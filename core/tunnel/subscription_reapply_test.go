package tunnel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

// appliedByOlderRelease is a config holding the bundle as an older release
// stored it: same revision, no schema.
func appliedByOlderRelease(t *testing.T, sub Subscription, bundle links.Bundle) Config {
	t.Helper()
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Subscriptions[0].Schema = 0
	return cfg
}

func TestOldCopyDownloadsOnceThenGetsNotModified(t *testing.T) {
	sub, key, bundle := subFixture(t)
	raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("If-None-Match"))
		mu.Unlock()
		if r.Header.Get("If-None-Match") == `"one"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"one"`)
		w.Write(raw)
	}))
	defer server.Close()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub.Sources = []string{server.URL}
	cfg := appliedByOlderRelease(t, sub, bundle)
	cfg.Subscriptions[0].Sources = sub.Sources
	cfg.Subscriptions[0].ETag, cfg.Subscriptions[0].ETagSource = `"one"`, server.URL

	var ref SubscriptionRefresher
	first := ref.Refresh(context.Background(), cfg, nil)
	got := first.Config.Subscriptions[0]
	if got.Schema != subscriptionSchema || got.LastError != "unchanged" {
		t.Fatalf("the old copy was not applied again: schema %d error %q", got.Schema, got.LastError)
	}
	if first.Changed {
		t.Fatal("an identical re-apply restarts the proxy")
	}
	second := ref.Refresh(context.Background(), first.Config, nil)
	if second.Changed || second.Config.Subscriptions[0].LastError != "unchanged" {
		t.Fatalf("second refresh: %q", second.Config.Subscriptions[0].LastError)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, []string{"", `"one"`}) {
		t.Fatalf("If-None-Match headers %q", seen)
	}
}

func TestNotModifiedWithoutConditionalRequestIsNotUnchanged(t *testing.T) {
	sub, _, bundle := subFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub.Sources = []string{server.URL}
	cfg := appliedByOlderRelease(t, sub, bundle)
	cfg.Subscriptions[0].Sources = sub.Sources
	cfg.Subscriptions[0].ETag, cfg.Subscriptions[0].ETagSource = `"one"`, server.URL
	var ref SubscriptionRefresher
	out := ref.Refresh(context.Background(), cfg, nil)
	if got := out.Config.Subscriptions[0]; got.LastError != "http" || got.Schema != 0 {
		t.Fatalf("a 304 nobody asked for: error %q schema %d", got.LastError, got.Schema)
	}
}

func TestSubscribeLinkKeepsSchema(t *testing.T) {
	sub, key, bundle := subFixture(t)
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil || cfg.Subscriptions[0].Schema != subscriptionSchema {
		t.Fatal("setup", err)
	}
	r, err := SubscribeLink(cfg, subInvite(t, sub, key, nil), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Config.Subscriptions[0]; got.Schema != subscriptionSchema || got.Revision != 1 {
		t.Fatalf("re-import: schema %d revision %d", got.Schema, got.Revision)
	}
}

func TestSubscriptionStateKeepsSchema(t *testing.T) {
	sub, _, bundle := subFixture(t)
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub}}, path)
	if err != nil || got.Subscriptions[0].Schema != subscriptionSchema {
		t.Fatalf("schema after restore: %v", err)
	}
}

func TestReapplyFromUntrustedSignerLeavesTrustedFieldsAlone(t *testing.T) {
	sub, _, bundle := subFixture(t) // the signer is not the trusted author
	old := appliedByOlderRelease(t, sub, bundle)
	bundle.Canaries = []string{"canary.example.org:443"}
	bundle.References = []string{"example.org:443"}
	bundle.Updates = []string{"https://updates.example.org"}
	cfg, err := ApplySubFile(old, old.Subscriptions[0], bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Canaries) != 0 || len(cfg.References) != 0 || len(cfg.UpdateSources) != 0 ||
		len(cfg.Subscriptions[0].Canaries) != 0 || len(cfg.Subscriptions[0].References) != 0 {
		t.Fatalf("untrusted file set canaries %v references %v updates %v", cfg.Canaries, cfg.References, cfg.UpdateSources)
	}
	if cfg.Subscriptions[0].Schema != subscriptionSchema {
		t.Fatal("schema not recorded")
	}
}

func TestReapplyKeepsLocalState(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	old := appliedByOlderRelease(t, sub, bundle)
	manual := subRoute("manual")
	old.Routes = links.Sorted(append(slices.Clone(old.Routes), manual))
	old.RouteEdits = map[string]RouteEdit{"one": {Off: true}}
	old.References = []string{"static.example.org:443"} // from a static link
	bundle.References = nil                             // the file says nothing about references
	cfg, err := ApplySubFile(old, old.Subscriptions[0], bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(cfg.Routes, func(r links.Route) bool { return r.ID == "manual" }) {
		t.Fatal("manual route lost")
	}
	if !cfg.RouteEdits["one"].Off {
		t.Fatalf("route edits lost: %v", cfg.RouteEdits)
	}
	if !slices.Equal(cfg.References, []string{"static.example.org:443"}) {
		t.Fatalf("static reference list replaced: %v", cfg.References)
	}
}

func TestClosedFileWithSameRevisionIsRefusedAgainstOldSchema(t *testing.T) {
	sub, _, bundle := subFixture(t)
	old := appliedByOlderRelease(t, sub, bundle)
	bundle.Closed = true
	cfg, err := ApplySubFile(old, old.Subscriptions[0], bundle)
	if !errors.Is(err, links.ErrNotNewer) || cfg.Subscriptions[0].Closed {
		t.Fatalf("a closing file at the same revision was applied: %v", err)
	}
}

func TestSeedAtNewerRevisionClearsETag(t *testing.T) {
	sub, key, bundle := subFixture(t)
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Subscriptions[0].ETag, cfg.Subscriptions[0].ETagSource = `"one"`, sub.Sources[0]
	bundle.Revision = 2
	seed, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	r, err := SubscribeLink(cfg, subInvite(t, sub, key, seed), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Config.Subscriptions[0]; got.Revision != 2 || got.ETag != "" || got.ETagSource != "" {
		t.Fatalf("revision %d etag %q source %q", got.Revision, got.ETag, got.ETagSource)
	}
	// A seed that is not newer keeps the tag.
	cfg.Subscriptions[0].ETag, cfg.Subscriptions[0].ETagSource = `"one"`, sub.Sources[0]
	bundle.Revision = 1
	old, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	r, err = SubscribeLink(cfg, subInvite(t, sub, key, old), false)
	if err != nil || r.Config.Subscriptions[0].ETag != `"one"` {
		t.Fatalf("tag dropped without a newer revision: %v", err)
	}
}

func TestSameRevisionReapplyDoesNotCancelNewerSource(t *testing.T) {
	cfg, raw, newer := subscriptionTestConfigNewer(t, "http://stale.example.org", "http://fresh.example.org")
	cfg.Subscriptions[0].Revision, cfg.Subscriptions[0].Schema = 1, 0
	synctest.Test(t, func(t *testing.T) {
		ref := SubscriptionRefresher{directDial: subscriptionTestDial(func(_ context.Context, req *http.Request) *http.Response {
			if req.Host == "stale.example.org" {
				return subscriptionTestResponse(http.StatusOK, string(raw))
			}
			return subscriptionTestResponse(http.StatusOK, string(newer))
		})}
		out := ref.Refresh(context.Background(), cfg, nil)
		if got := out.Config.Subscriptions[0]; got.Revision != 2 || got.Schema != subscriptionSchema {
			t.Fatalf("a lagging source hid the newer file: revision %d schema %d", got.Revision, got.Schema)
		}
	})
}

func TestAnsweredSourceIsNotDialledDirectlyAgain(t *testing.T) {
	cfg, raw := subscriptionTestConfig(t, "http://example.org")
	synctest.Test(t, func(t *testing.T) {
		directDials := 0
		ok := subscriptionTestDial(func(context.Context, *http.Request) *http.Response {
			return subscriptionTestResponse(http.StatusOK, string(raw))
		})
		ref := SubscriptionRefresher{directDial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			directDials++
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		tunnel := func(ctx context.Context, network, address string) (net.Conn, error) { return ok(ctx, network, address) }
		out := ref.Refresh(context.Background(), cfg, []SubscriptionDial{tunnel}) // marks the direct path
		cfg = out.Config
		before := time.Now()
		out = ref.Refresh(context.Background(), cfg, []SubscriptionDial{tunnel}) // same revision: unchanged
		synctest.Wait()
		if directDials != 1 || time.Since(before) != 0 || out.Config.Subscriptions[0].LastError != "unchanged" {
			t.Fatalf("direct dials %d, took %v, state %q", directDials, time.Since(before), out.Config.Subscriptions[0].LastError)
		}
	})
}

func TestSameAppliedIgnoresNilVersusEmpty(t *testing.T) {
	a := Config{Subscriptions: []Subscription{{ID: "x"}}}
	b := Config{Subscriptions: []Subscription{{ID: "x"}}, RouteEdits: map[string]RouteEdit{}, Routes: []links.Route{}}
	if !sameApplied(a, b, 0) {
		t.Fatal("nil and empty edits or routes count as a change")
	}
	b.RouteEdits = map[string]RouteEdit{"one": {Off: true}}
	if sameApplied(a, b, 0) {
		t.Fatal("a real edit is ignored")
	}
}
