package tunnel

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"path/filepath"
	"slices"
	"testing"

	links "github.com/mrgluek/passage-link/link"
)

// On start the manual routes (link.txt) are loaded first and the saved
// subscription state is restored over them.
func restoreAtStart(t *testing.T, trusted bool, manual []links.Route, other *Subscription) (Config, Subscription) {
	t.Helper()
	s, _, b := subFixture(t)
	old := AuthorKey
	t.Cleanup(func() { AuthorKey = old })
	if trusted {
		AuthorKey = base64.RawURLEncoding.EncodeToString(s.Signer)
	} else {
		AuthorKey = ""
	}
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{s}}, s, b)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	start := Config{Routes: slices.Clone(manual), Subscriptions: slices.Clone(cfg.Subscriptions)}
	start.Subscriptions[0].RouteIDs = nil
	if other != nil {
		start.Subscriptions = append([]Subscription{*other}, start.Subscriptions...)
	}
	got, err := RestoreSubscriptionState(start, path)
	if err != nil {
		t.Fatal(err)
	}
	return got, got.Subscriptions[len(got.Subscriptions)-1]
}

func routeByID(cfg Config, id string) (links.Route, bool) {
	for _, r := range cfg.Routes {
		if r.ID == id {
			return r, true
		}
	}
	return links.Route{}, false
}

func TestRestoreTrustedSubscriptionTakesOverManualRoute(t *testing.T) {
	manual := subRoute("one")
	manual.Priority = 5
	got, sub := restoreAtStart(t, true, []links.Route{manual}, nil)
	r, ok := routeByID(got, "one")
	if !ok || r.Priority != 100 || !slices.Equal(sub.RouteIDs, []string{"one"}) || sub.LastError != "" {
		t.Fatalf("takeover failed: route %+v owned %v error %q", r, sub.RouteIDs, sub.LastError)
	}
	if len(got.Routes) != 1 {
		t.Fatalf("routes %d, want 1 (no duplicate)", len(got.Routes))
	}
}

func TestRestoreUntrustedSubscriptionKeepsManualRoute(t *testing.T) {
	manual := subRoute("one")
	manual.Priority = 5
	got, sub := restoreAtStart(t, false, []links.Route{manual}, nil)
	r, _ := routeByID(got, "one")
	if r.Priority != 5 || len(sub.RouteIDs) != 0 || sub.LastError != "collision" {
		t.Fatalf("manual route must stay: priority %d owned %v error %q", r.Priority, sub.RouteIDs, sub.LastError)
	}
}

func TestRestoreKeepsManualRoutesWithOtherIDs(t *testing.T) {
	got, sub := restoreAtStart(t, true, []links.Route{subRoute("extra")}, nil)
	_, extra := routeByID(got, "extra")
	_, one := routeByID(got, "one")
	if !extra || !one || !slices.Equal(sub.RouteIDs, []string{"one"}) || sub.LastError != "" {
		t.Fatalf("both routes must be there: extra %v one %v owned %v error %q", extra, one, sub.RouteIDs, sub.LastError)
	}
}

func TestRestoreDoesNotTakeOverRouteOfAnotherSubscription(t *testing.T) {
	// The route "one" already belongs to another subscription at this point.
	foreign, _, _ := subFixture(t)
	foreign.ID = hex.EncodeToString(bytes.Repeat([]byte{2}, 16))
	foreign.Name = "Other"
	foreign.RouteIDs = []string{"one"}
	owned := subRoute("one")
	owned.Priority = 7
	got, sub := restoreAtStart(t, true, []links.Route{owned}, &foreign)
	r, _ := routeByID(got, "one")
	if r.Priority != 7 || len(sub.RouteIDs) != 0 || sub.LastError != "collision" {
		t.Fatalf("another subscription's route must stay: priority %d owned %v error %q", r.Priority, sub.RouteIDs, sub.LastError)
	}
}
