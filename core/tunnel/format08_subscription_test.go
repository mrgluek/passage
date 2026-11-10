package tunnel

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	links "github.com/mrgluek/passage-link/link"
)

func trustSubscription(t *testing.T, s Subscription) {
	t.Helper()
	old := AuthorKey
	AuthorKey = base64.RawURLEncoding.EncodeToString(s.Signer)
	t.Cleanup(func() { AuthorKey = old })
}

func TestBundleSourcesSuccessiveRefreshesAndRestart(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	invite := Config{Subscriptions: []Subscription{sub}}
	cfg := invite
	for i, tc := range []struct{ sources, want []string }{
		{[]string{"https://example.org/first", sub.Sources[0]}, []string{"https://example.org/first", sub.Sources[0]}},
		{[]string{"https://example.org/second"}, []string{"https://example.org/second", sub.Sources[0]}},
		{nil, sub.Sources},
	} {
		bundle.Revision = uint64(i + 1)
		bundle.Sources = tc.sources
		next, err := ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(next.Subscriptions[0].Sources, tc.want) {
			t.Fatalf("refresh %d: sources %v", i, next.Subscriptions[0].Sources)
		}
		if !slices.Equal(next.Subscriptions[0].InviteSources, sub.Sources) {
			t.Fatal("invite sources lost")
		}
		path := filepath.Join(t.TempDir(), "subscriptions.json")
		next.Subscriptions[0].ETag = "saved"
		next.Subscriptions[0].ETagSource = tc.want[0]
		if err = SaveSubscriptionState(path, next); err != nil {
			t.Fatal(err)
		}
		cfg, err = RestoreSubscriptionState(invite, path)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.Subscriptions[0].Sources, tc.want) || cfg.Subscriptions[0].ETag != "saved" {
			t.Fatalf("restart %d discarded sources or ETag", i)
		}
	}
}

func TestBundleSourcesCapAndInviteReimport(t *testing.T) {
	sub, key, bundle := subFixture(t)
	trustSubscription(t, sub)
	sub.Sources = []string{"https://example.org/i1", "https://example.org/i2", "https://example.org/i3", "https://example.org/i4"}
	bundle.Sources = []string{"https://example.org/b1", "https://example.org/b2", "https://example.org/b3", "https://example.org/b4"}
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Concat(bundle.Sources, sub.Sources)
	if !slices.Equal(cfg.Subscriptions[0].Sources, want) {
		t.Fatal("eight-source merge")
	}
	result, err := SubscribeLink(cfg, subInvite(t, sub, key, nil), false)
	if err != nil || !slices.Equal(result.Config.Subscriptions[0].Sources, want) {
		t.Fatal("reimport lost bundle sources", err)
	}
	if got := mergeSubscriptionSources(want, []string{"https://example.org/overflow"}); !slices.Equal(got, want) {
		t.Fatal("source cap")
	}
}

func TestBundleSettingsTrustRevisionAndClosed(t *testing.T) {
	sub, _, bundle := subFixture(t)
	_, other, _ := ed25519.GenerateKey(nil)
	old := AuthorKey
	AuthorKey = base64.RawURLEncoding.EncodeToString(other.Public().(ed25519.PublicKey))
	t.Cleanup(func() { AuthorKey = old })
	bundle.Sources = []string{"https://example.org/new"}
	bundle.Canaries = []string{"example.org:993"}
	cfg := Config{Subscriptions: []Subscription{sub}, Canaries: []string{"example.org:443"}}
	got, err := ApplySubFile(cfg, sub, bundle)
	if err != nil || !slices.Equal(got.Subscriptions[0].Sources, sub.Sources) || !slices.Equal(got.Canaries, cfg.Canaries) {
		t.Fatal("unknown signer changed settings", err)
	}
	AuthorKey = base64.RawURLEncoding.EncodeToString(sub.Signer)
	got, err = ApplySubFile(cfg, sub, bundle)
	if err != nil || !slices.Equal(got.Canaries, bundle.Canaries) {
		t.Fatal("trusted settings", err)
	}
	for _, revision := range []uint64{0, 1} {
		bundle.Revision = revision
		bundle.Sources = []string{"https://example.org/replayed"}
		bundle.Canaries = []string{"example.org:995"}
		rejected, err := ApplySubFile(got, got.Subscriptions[0], bundle)
		if err == nil || !reflect.DeepEqual(rejected, got) {
			t.Fatal("replay changed settings", err)
		}
	}
	bundle.Revision, bundle.Closed, bundle.Routes = 2, true, nil
	bundle.Sources, bundle.Canaries = nil, nil
	closed, err := ApplySubFile(got, got.Subscriptions[0], bundle)
	if err != nil || !slices.Equal(closed.Subscriptions[0].Sources, got.Subscriptions[0].Sources) || !slices.Equal(closed.Canaries, got.Canaries) {
		t.Fatal("closed changed settings", err)
	}
}

func TestBundleCanariesPersistAndEmptyPreserves(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	bundle.Canaries = []string{"example.org:993", "example.org:443"}
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub}}, path)
	if err != nil || !slices.Equal(cfg.Canaries, bundle.Canaries) {
		t.Fatal("canaries not restored", err)
	}
	want := slices.Clone(cfg.Canaries)
	for _, canaries := range [][]string{nil, {}} {
		bundle.Revision++
		bundle.Canaries = canaries
		cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
		if err != nil || !slices.Equal(cfg.Canaries, want) {
			t.Fatal("empty canaries changed list", err)
		}
	}
}

func TestCollectionCanariesTrustAndBaseline(t *testing.T) {
	sub, key, _ := subFixture(t)
	_, other, _ := ed25519.GenerateKey(nil)
	trustSubscription(t, sub)
	initial := Config{Canaries: []string{"example.org:443"}}
	for _, tc := range []struct {
		name     string
		signer   ed25519.PrivateKey
		canaries []string
		want     []string
	}{
		{"baseline", key, nil, initial.Canaries},
		{"trusted", key, []string{"example.org:993"}, []string{"example.org:993"}},
		{"unknown signer", other, []string{"example.org:993"}, initial.Canaries},
		{"unsigned", nil, []string{"example.org:993"}, initial.Canaries},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := links.Bundle{V: 2, Kind: "routes", Mode: "merge", Routes: []links.Route{subRoute("one")}, Canaries: tc.canaries}
			raw, err := links.Encode(bundle, tc.signer)
			if err != nil {
				t.Fatal(err)
			}
			uri, err := links.URI(raw, "")
			if err != nil {
				t.Fatal(err)
			}
			result, err := ImportLink(initial, uri, false)
			if err != nil || !slices.Equal(result.Config.Canaries, tc.want) {
				t.Fatal("collection canaries", err)
			}
			if tc.name == "baseline" {
				want, err := (Config{Routes: bundle.Routes, Canaries: initial.Canaries, RouteEdits: map[string]RouteEdit{}}).Normalize()
				if err != nil || !reflect.DeepEqual(result.Config, want) {
					t.Fatal("baseline behavior changed", err)
				}
			}
		})
	}
}

func TestBundleWithoutNewFieldsPreservesSubscriptionSettings(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	original := Config{Subscriptions: []Subscription{sub}, Canaries: []string{"example.org:443"}}
	cfg, err := ApplySubFile(original, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Subscriptions[0].Sources, sub.Sources) || len(cfg.Subscriptions[0].BundleSources) != 0 || len(cfg.Subscriptions[0].Canaries) != 0 || !slices.Equal(cfg.Canaries, original.Canaries) || !reflect.DeepEqual(cfg.Routes, bundle.Routes) {
		t.Fatal("legacy bundle behavior changed")
	}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	original.Canaries = []string{"example.org:993"}
	restored, err := RestoreSubscriptionState(original, path)
	if err != nil || !slices.Equal(restored.Canaries, original.Canaries) {
		t.Fatal("bundle without canaries replaced current settings on restart", err)
	}
}

func TestBundleSourcesThroughVerifiedRefresh(t *testing.T) {
	sub, key, bundle := subFixture(t)
	trustSubscription(t, sub)
	oldHTTP := subscriptionTestHTTP
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = oldHTTP })
	var mu sync.Mutex
	var blob []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/invite/"+sub.ID+".bin" {
			t.Error("wrong subscription path")
		}
		mu.Lock()
		defer mu.Unlock()
		w.Write(blob)
	}))
	defer server.Close()
	sub.Sources = []string{server.URL + "/invite"}
	cfg := Config{Subscriptions: []Subscription{sub}}
	refresher := &SubscriptionRefresher{directDial: func(ctx context.Context, n, a string) (net.Conn, error) {
		if strings.HasPrefix(a, "example.org:") {
			return nil, errors.New("unavailable")
		}
		return (&net.Dialer{}).DialContext(ctx, n, a)
	}}
	for i, sources := range [][]string{{"https://example.org/first"}, {"https://example.org/second"}, nil} {
		bundle.Revision = uint64(i + 1)
		bundle.Sources = sources
		raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		blob = raw
		mu.Unlock()
		result := refresher.Refresh(context.Background(), cfg, nil)
		if !result.Changed || result.Config.Subscriptions[0].Revision != bundle.Revision {
			t.Fatal("verified refresh failed")
		}
		cfg = result.Config
		want := mergeSubscriptionSources(sources, sub.Sources)
		if !slices.Equal(cfg.Subscriptions[0].Sources, want) {
			t.Fatalf("refresh %d lost sources", i)
		}
	}
}
