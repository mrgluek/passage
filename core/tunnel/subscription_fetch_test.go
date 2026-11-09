package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	links "github.com/mrgluek/passage-link/link"
	"golang.org/x/crypto/chacha20poly1305"
)

func TestSubscriptionFetchFallbackAnd304(t *testing.T) {
	s, key, b := subFixture(t)
	raw, err := links.SealSubFile(b, b.Sub, s.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var requests, conditional atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/base/"+s.ID+".bin" {
			t.Error("wrong path")
		}
		if r.Header.Get("If-None-Match") == `"one"` {
			conditional.Add(1)
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"one"`)
		w.Write(raw)
	}))
	defer server.Close()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	s.Sources = []string{server.URL + "/base"}
	var tried atomic.Int32
	broken := func(context.Context, string, string) (net.Conn, error) {
		tried.Add(1)
		return nil, errors.New("unavailable")
	}
	var ref SubscriptionRefresher
	cfg := Config{Subscriptions: []Subscription{s}}
	out := ref.Refresh(context.Background(), cfg, []SubscriptionDial{broken})
	if !out.Changed || len(out.Config.Routes) != 1 || out.Config.Subscriptions[0].ETag != `"one"` || tried.Load() > 1 {
		t.Fatal("fallback failed")
	}
	second := ref.Refresh(context.Background(), out.Config, []SubscriptionDial{broken})
	if second.Changed || conditional.Load() != 1 || second.Config.Subscriptions[0].LastError != "unchanged" || second.Config.Subscriptions[0].LastOK == 0 {
		t.Fatal("304 failed")
	}
	if requests.Load() != 2 {
		t.Fatal("unexpected attempts")
	}
	// A lower valid revision must leave the installed routes and revision intact.
	out.Config.Subscriptions[0].Revision = 2
	out.Config.Subscriptions[0].ETag = ""
	rollback := ref.Refresh(context.Background(), out.Config, nil)
	if rollback.Changed || rollback.Config.Subscriptions[0].LastError != "rollback" || rollback.Config.Subscriptions[0].Revision != 2 {
		t.Fatal("rollback accepted")
	}
}

func TestSubscriptionFetchRefusesRedirectAndOversize(t *testing.T) {
	s, _, _ := subFixture(t)
	if _, _, code := fetchSubscription(context.Background(), s, "http://example.org/sub", (&net.Dialer{}).DialContext); code != "http" {
		t.Fatal("HTTP allowed by default")
	}
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	var followed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect/" + s.ID + ".bin":
			w.Header().Set("Location", "/target")
			w.WriteHeader(302)
		case "/target":
			followed.Add(1)
		default:
			w.Write(bytes.Repeat([]byte{0}, links.MaxSubFileBytes+links.SubFileOverhead+1))
		}
	}))
	defer server.Close()
	dial := (&net.Dialer{}).DialContext
	if _, _, code := fetchSubscription(context.Background(), s, server.URL+"/redirect", dial); code != "http" || followed.Load() != 0 {
		t.Fatal(code)
	}
	if _, _, code := fetchSubscription(context.Background(), s, server.URL+"/large", dial); code != "size" {
		t.Fatal(code)
	}
	if _, _, code := fetchSubscription(context.Background(), s, "http://example.org/?query", dial); code != "http" {
		t.Fatal(code)
	}
}

func TestSubscriptionAuthentication(t *testing.T) {
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	s, key, b := subFixture(t)
	blob, err := links.SealSubFile(b, b.Sub, s.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	wrongPub, _, _ := ed25519.GenerateKey(nil)
	for _, tc := range []struct {
		name           string
		blob, key, pub []byte
	}{
		{"wrong signer", blob, s.ReadKey, wrongPub},
		{"wrong key", blob, bytes.Repeat([]byte{4}, 32), s.Signer},
		{"tampered", append(append([]byte{}, blob[:len(blob)-1]...), blob[len(blob)-1]^1), s.ReadKey, s.Signer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := links.OpenSubFile(tc.blob, b.Sub, tc.key, ed25519.PublicKey(tc.pub)); err == nil {
				t.Fatal("accepted invalid file")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(tc.blob) }))
			defer server.Close()
			bad := s
			bad.Sources = []string{server.URL}
			bad.Signer = tc.pub
			bad.ReadKey = tc.key
			var fetcher SubscriptionRefresher
			out := fetcher.Refresh(context.Background(), Config{Subscriptions: []Subscription{bad}}, nil)
			if out.Changed || len(out.Config.Routes) != 0 || out.Config.Subscriptions[0].LastError == "" {
				t.Fatal("fetch activated invalid file")
			}
		})
	}
}

func TestSubscriptionFetchRequiredFeature(t *testing.T) {
	s, key, b := subFixture(t)
	raw, err := links.Encode(b, key)
	if err != nil {
		t.Fatal(err)
	}
	var tag cbor.Tag
	if err = cbor.Unmarshal(raw, &tag); err != nil {
		t.Fatal(err)
	}
	parts := tag.Content.([]any)
	var fields map[string]cbor.RawMessage
	if err = cbor.Unmarshal(parts[2].([]byte), &fields); err != nil {
		t.Fatal(err)
	}
	enc, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		t.Fatal(err)
	}
	fields["required"], err = enc.Marshal([]string{"future"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := enc.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	toSign, err := enc.Marshal([]any{"Signature1", parts[0], []byte("passage-link-v2"), body})
	if err != nil {
		t.Fatal(err)
	}
	parts[2], parts[3] = body, ed25519.Sign(key, toSign)
	signed, err := enc.Marshal(cbor.Tag{Number: 18, Content: parts})
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 1024)
	if len(signed) > len(plain)-4 {
		t.Fatal("fixture too large")
	}
	binary.BigEndian.PutUint32(plain, uint32(len(signed)))
	copy(plain[4:], signed)
	header := append([]byte("PSUB1"), bytes.Repeat([]byte{5}, 24)...)
	aead, err := chacha20poly1305.NewX(s.ReadKey)
	if err != nil {
		t.Fatal(err)
	}
	blob := aead.Seal(header, header[5:], plain, append([]byte("PSUB1"), b.Sub...))
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(blob) }))
	defer server.Close()
	s.Sources = []string{server.URL}
	var fetcher SubscriptionRefresher
	out := fetcher.Refresh(context.Background(), Config{Subscriptions: []Subscription{s}}, nil)
	if out.Changed || out.Config.Subscriptions[0].LastError != "required" {
		t.Fatal("required feature accepted")
	}
}

func TestClosedSubscriptionLifecycle(t *testing.T) {
	s, key, b := subFixture(t)
	s.Revision, s.RouteIDs, s.LastError = 1, []string{"one"}, "network"
	other := s
	other.ID = "02020202020202020202020202020202"
	other.RouteIDs = []string{"other"}
	cfg := Config{Subscriptions: []Subscription{s, other}, Routes: []links.Route{subRoute("one"), subRoute("manual"), subRoute("other")}, RouteEdits: map[string]RouteEdit{"one": {Off: true}, "manual": {Off: true}, "other": {Off: true}}}
	b.Revision, b.Closed, b.Routes = 2, true, nil
	blob, err := links.SealSubFile(b, b.Sub, s.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+other.ID+".bin" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		requests.Add(1)
		w.Write(blob)
	}))
	defer server.Close()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	cfg.Subscriptions[0].Sources = []string{server.URL}
	cfg.Subscriptions[1].Sources = []string{server.URL}
	var ref SubscriptionRefresher
	out := ref.Refresh(context.Background(), cfg, nil)
	closed := out.Config.Subscriptions[0]
	if !out.Changed || !closed.Closed || closed.Revision != 2 || closed.LastOK == 0 || closed.LastError != "" || len(closed.RouteIDs) != 0 {
		t.Fatal("closed metadata", out)
	}
	if len(out.Config.Routes) != 2 || out.Config.Routes[0].ID == "one" || len(out.Config.RouteEdits) != 2 || out.Config.RouteEdits["one"].Off {
		t.Fatal("ownership", out.Config)
	}
	if len(cfg.Routes) != 3 || !cfg.RouteEdits["one"].Off || cfg.Subscriptions[0].Closed {
		t.Fatal("mutated input")
	}
	again := ref.Refresh(context.Background(), out.Config, nil)
	if again.Changed || requests.Load() != 1 || again.Config.Subscriptions[0].LastTry != closed.LastTry {
		t.Fatal("fetched closed subscription")
	}
	b.Closed, b.Routes, b.Revision = false, []links.Route{subRoute("one")}, 1
	if _, err := ApplySubFile(out.Config, closed, b); !errors.Is(err, links.ErrRollback) {
		t.Fatal("stale open", err)
	}
	b.Revision = 3
	if _, err := ApplySubFile(out.Config, closed, b); err == nil || err.Error() != "closed" {
		t.Fatal("reopened", err)
	}
	if _, err := SubscribeLink(out.Config, subInvite(t, s, key, nil), false); err == nil || err.Error() != "closed" {
		t.Fatal("re-imported", err)
	}
	rows := subscriptionStatuses(out.Config)
	if !rows[0].Closed {
		t.Fatal("public status")
	}
	removed, err := RemoveSubscription(out.Config, s.ID)
	if err != nil || len(removed.Subscriptions) != 1 || len(removed.Routes) != 2 {
		t.Fatal("remove", err)
	}
}

func TestSubscriptionFetchDoesNotWaitForTunnel(t *testing.T) {
	sub, key, bundle := subFixture(t)
	raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-entered; w.Write(raw) }))
	defer server.Close()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub.Sources = []string{server.URL}
	canceled := make(chan struct{})
	slow := func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var ref SubscriptionRefresher
	out := ref.Refresh(ctx, Config{Subscriptions: []Subscription{sub}}, []SubscriptionDial{slow})
	if !out.Changed || ctx.Err() != nil {
		t.Fatal("direct waited for tunnel", out)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("losing dial not canceled")
	}
}
