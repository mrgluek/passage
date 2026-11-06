package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	links "github.com/mrgluek/passage-link/link"
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
	if !out.Changed || len(out.Config.Routes) != 1 || out.Config.Subscriptions[0].ETag != `"one"` || tried.Load() != 1 {
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
		})
	}
}
