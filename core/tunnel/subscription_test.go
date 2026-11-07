package tunnel

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"testing"

	links "github.com/mrgluek/passage-link/link"
)

func subFixture(t *testing.T) (Subscription, ed25519.PrivateKey, links.Bundle) {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(nil)
	s := Subscription{ID: hex.EncodeToString(bytes.Repeat([]byte{1}, 16)), Name: "Example", Signer: pub, Sources: []string{"https://example.org/sub"}, ReadKey: bytes.Repeat([]byte{2}, 32)}
	b := links.Bundle{V: 2, Kind: "routes", Mode: "replace", Sub: bytes.Repeat([]byte{1}, 16), Revision: 1, Signer: pub, Routes: []links.Route{subRoute("one")}}
	return s, key, b
}
func subRoute(id string) links.Route {
	return links.Route{ID: id, Type: "wss", Wire: "wss-v1", Priority: 100, Params: map[string]any{"endpoint": "wss://example.org/route", "key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))}}
}
func subInvite(t *testing.T, s Subscription, key ed25519.PrivateKey, seed []byte) string {
	t.Helper()
	id, _ := hex.DecodeString(s.ID)
	raw, err := links.EncodeInvite(links.Invite{V: 2, Kind: "sub", ID: id, Name: s.Name, Sources: s.Sources, ReadKey: s.ReadKey, Required: []string{"sub"}, Seed: seed}, key)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := links.InviteURI(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	return uri
}
func TestSubscriptionSeedAndSignerCollision(t *testing.T) {
	s, key, b := subFixture(t)
	old := AuthorKey
	AuthorKey = base64.RawURLEncoding.EncodeToString(s.Signer)
	t.Cleanup(func() { AuthorKey = old })
	seed, err := links.SealSubFile(b, b.Sub, s.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	uri := subInvite(t, s, key, seed)
	preview, err := ImportLink(Config{}, uri, false)
	if err != nil || preview.Signature != "trusted_author" || preview.Mode != "subscribe" || preview.Subscription == nil || !preview.Subscription.HasSeed || len(preview.Config.Subscriptions) != 0 {
		t.Fatal(preview, err)
	}
	result, err := SubscribeLink(Config{}, uri, false)
	if err != nil || len(result.Config.Routes) != 1 || result.Config.Subscriptions[0].Revision != 1 {
		t.Fatal(result, err)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	collision, err := SubscribeLink(result.Config, subInvite(t, s, other, nil), false)
	if err != nil || collision.Signature != "unknown_signer" || !collision.Collision || !bytes.Equal(collision.Config.Subscriptions[0].Signer, s.Signer) {
		t.Fatal("signer collision", err)
	}
	again, err := SubscribeLink(result.Config, uri, false)
	if err != nil || again.Config.Subscriptions[0].Revision != 1 {
		t.Fatal("repeated seed", err)
	}
}
func TestSubscriptionApplyOwnershipEditsAndRevision(t *testing.T) {
	s, _, b := subFixture(t)
	priority := uint16(7)
	s.RouteIDs = []string{"one", "gone"}
	cfg := Config{Subscriptions: []Subscription{s}, Routes: []links.Route{subRoute("one"), subRoute("gone"), subRoute("manual")}, RouteEdits: map[string]RouteEdit{"one": {Priority: &priority, Off: true}, "gone": {Off: true}}}
	b.Routes = append(b.Routes, subRoute("manual"), subRoute("new"))
	got, err := ApplySubFile(cfg, s, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) != 3 || got.Subscriptions[0].LastError != "collision" || len(got.RouteEdits) != 1 || !got.RouteEdits["one"].Off || *got.RouteEdits["one"].Priority != 7 {
		t.Fatal("ownership or edits")
	}
	if !reflect.DeepEqual(cfg.Subscriptions[0].RouteIDs, []string{"one", "gone"}) {
		t.Fatal("mutated input")
	}
	if _, err = ApplySubFile(got, got.Subscriptions[0], b); !errors.Is(err, links.ErrNotNewer) {
		t.Fatal(err)
	}
	b.Revision = 0
	if _, err = ApplySubFile(got, got.Subscriptions[0], b); !errors.Is(err, links.ErrRollback) {
		t.Fatal(err)
	}
	b.Revision = 2
	b.Required = []string{"future"}
	if _, err = ApplySubFile(got, got.Subscriptions[0], b); subscriptionError(err) != "required" {
		t.Fatal(err)
	}
}
func TestSubscriptionUpdatesAndRouteLimit(t *testing.T) {
	s, _, b := subFixture(t)
	old := AuthorKey
	t.Cleanup(func() { AuthorKey = old })
	b.Updates = []string{"https://example.org/releases"}
	cfg := Config{Subscriptions: []Subscription{s}}
	got, err := ApplySubFile(cfg, s, b)
	if err != nil || len(got.UpdateSources) != 0 {
		t.Fatal(err)
	}
	AuthorKey = base64.RawURLEncoding.EncodeToString(s.Signer)
	b.Routes = nil
	for i := 0; i < links.MaxSubRoutes; i++ {
		b.Routes = append(b.Routes, subRoute(fmt.Sprintf("route-%d", i)))
	}
	got, err = ApplySubFile(cfg, s, b)
	if err != nil || len(got.Routes) != 16 || len(got.UpdateSources) != 1 {
		t.Fatal(err)
	}
	pool, err := newRoutePool(got)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if len(pool.(*routePool).clients) != 16 {
		t.Fatal("pool cap")
	}
	b.Routes = append(b.Routes, subRoute("extra"))
	if _, err = b.Validate(); err == nil {
		t.Fatal("subscription route cap lifted")
	}
	b.Routes = b.Routes[:16]
	b.Sub = nil
	b.Revision = 0
	if _, err = b.Validate(); err == nil {
		t.Fatal("plain route cap lifted")
	}
}

func TestRemoveSubscription(t *testing.T) {
	sub, _, _ := subFixture(t)
	sub.RouteIDs = []string{"one", "shared"}
	other := sub
	other.ID = "other"
	other.RouteIDs = []string{"shared"}
	cfg := Config{Subscriptions: []Subscription{sub, other}, Routes: []links.Route{subRoute("one"), subRoute("manual"), subRoute("shared")}, RouteEdits: map[string]RouteEdit{"one": {Off: true}, "manual": {Off: true}, "shared": {Off: true}}}
	before := fmt.Sprintf("%#v", cfg)
	got, err := RemoveSubscription(cfg, sub.ID)
	if err != nil || len(got.Subscriptions) != 1 || len(got.Routes) != 2 || got.Routes[0].ID != "manual" || len(got.RouteEdits) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if fmt.Sprintf("%#v", cfg) != before {
		t.Fatal("input changed")
	}
	if _, err := RemoveSubscription(cfg, "missing"); err == nil {
		t.Fatal("missing ID accepted")
	}
}

func TestSubscriptionManualRouteTakeover(t *testing.T) {
	old := AuthorKey
	t.Cleanup(func() { AuthorKey = old })
	for _, tc := range []struct {
		name                string
		trusted, otherOwner bool
	}{
		{"trusted author", true, false},
		{"unknown signer", false, false},
		{"another subscription", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, key, bundle := subFixture(t)
			pub, _, _ := ed25519.GenerateKey(nil)
			AuthorKey = base64.RawURLEncoding.EncodeToString(pub)
			if tc.trusted {
				AuthorKey = base64.RawURLEncoding.EncodeToString(s.Signer)
			}
			manual := subRoute("one")
			manual.Params["endpoint"] = "wss://example.org/old"
			priority := uint16(7)
			cfg := Config{Subscriptions: []Subscription{s}, Routes: []links.Route{manual}, RouteEdits: map[string]RouteEdit{"one": {Off: true, Priority: &priority}}}
			if tc.otherOwner {
				other := s
				other.ID = hex.EncodeToString(bytes.Repeat([]byte{4}, 16))
				other.RouteIDs = []string{"one"}
				cfg.Subscriptions = append(cfg.Subscriptions, other)
			}
			got, err := ApplySubFile(cfg, s, bundle)
			if err != nil {
				t.Fatal(err)
			}
			takeover := tc.trusted && !tc.otherOwner
			wantRoute, wantError := manual, "collision"
			var wantIDs []string
			if takeover {
				wantRoute, wantError, wantIDs = bundle.Routes[0], "", []string{"one"}
			}
			if !reflect.DeepEqual(got.Routes, []links.Route{wantRoute}) || !reflect.DeepEqual(got.Subscriptions[0].RouteIDs, wantIDs) || got.Subscriptions[0].LastError != wantError {
				t.Fatalf("unexpected routes or ownership: %+v", got)
			}
			if !reflect.DeepEqual(got.RouteEdits, cfg.RouteEdits) || !reflect.DeepEqual(cfg.Routes, []links.Route{manual}) || len(cfg.Subscriptions[0].RouteIDs) != 0 {
				t.Fatal("edits lost or input changed")
			}
			again, err := ApplySubFile(got, got.Subscriptions[0], bundle)
			if !errors.Is(err, links.ErrNotNewer) || !reflect.DeepEqual(again, got) {
				t.Fatal("repeated file changed config", err)
			}
			removed, err := RemoveSubscription(got, s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if takeover {
				if len(removed.Routes) != 0 || len(removed.RouteEdits) != 0 {
					t.Fatal("taken-over route survived removal")
				}
				raw, err := links.Encode(links.Bundle{V: 2, Kind: "routes", Mode: "merge", Routes: []links.Route{manual}}, key)
				if err != nil {
					t.Fatal(err)
				}
				uri, err := links.URI(raw, "")
				if err != nil {
					t.Fatal(err)
				}
				imported, err := ImportLink(got, uri, true)
				if err != nil || len(imported.Config.Subscriptions[0].RouteIDs) != 0 {
					t.Fatal("plain import did not detach ownership", err)
				}
				detached, err := RemoveSubscription(imported.Config, s.ID)
				if err != nil || !reflect.DeepEqual(detached.Routes, []links.Route{manual}) {
					t.Fatal("plain import route removed with subscription", err)
				}
			} else if !reflect.DeepEqual(removed.Routes, []links.Route{manual}) || !reflect.DeepEqual(removed.RouteEdits, cfg.RouteEdits) {
				t.Fatal("unowned route removed")
			}
		})
	}
}
