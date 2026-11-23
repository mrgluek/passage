package tunnel

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

// limitedTunnel has a limited verdict shown and a fake clock.
func limitedTunnel(t *testing.T) (*Tunnel, *group, *time.Time) {
	t.Helper()
	tt, g, _ := refTunnel(t, testReferences, testCanaries[0])
	now := time.Now()
	tt.now = func() time.Time { return now }
	tt.check()
	waitPass(t, tt)
	tt.check()
	if !tt.Status(0).NetworkLimited {
		t.Fatal("flag not set")
	}
	return tt, g, &now
}

func TestLimitedFlagIgnoresWhichGroupIsMail(t *testing.T) {
	tt, g, now := limitedTunnel(t)
	// A new relay group appears: zero last result, most recently used.
	other := tt.testGroup("new.example.org:443")
	tt.mu.Lock()
	other.used = now.Add(time.Minute)
	tt.mu.Unlock()
	if !tt.Status(0).NetworkLimited {
		t.Fatal("a fresh group cleared the flag")
	}
	// A probe-blind group is skipped; its last result is empty.
	tt.mu.Lock()
	other.used = time.Time{}
	g.used = now.Add(time.Minute)
	g.lastResult = probeResult{}
	g.probeBlind, g.probeBlindAt, g.lastProbe = true, *now, *now
	tt.mu.Unlock()
	tt.check()
	if !tt.Status(0).NetworkLimited {
		t.Fatal("a probe-blind group cleared the flag")
	}
	// Stale verdicts and manual mode still clear it.
	*now = now.Add(refFresh + refGrace + time.Second)
	if tt.Status(0).NetworkLimited {
		t.Fatal("stale verdict still shown")
	}
	*now = now.Add(-refFresh - refGrace)
	if !tt.Status(0).NetworkLimited {
		t.Fatal("fresh verdict hidden")
	}
	if err := tt.SetMode(ModeOff); err != nil || tt.Status(0).NetworkLimited {
		t.Fatal("manual mode keeps the flag", err)
	}
}

func TestNoReferencePassAfterModeLeavesAuto(t *testing.T) {
	tt, _, _ := refTunnel(t, testReferences)
	inCanary, release := make(chan struct{}), make(chan struct{})
	dials := make(chan string, 16)
	tt.probeTLS = func(ctx context.Context, hp, _ string) error {
		if hp == testCanaries[0] {
			close(inCanary)
			<-release
			return nil
		}
		dials <- hp
		return errors.New("blocked")
	}
	done := make(chan struct{})
	go func() { tt.probe(); close(done) }()
	<-inCanary
	if err := tt.SetMode(ModeOff); err != nil {
		t.Fatal(err)
	}
	for len(dials) > 0 { // relay dials before the canary are fine
		<-dials
	}
	close(release)
	<-done
	waitPass(t, tt)
	time.Sleep(50 * time.Millisecond)
	for len(dials) > 0 {
		if h := <-dials; slices.Contains(testReferences, h) {
			t.Fatalf("reference dial %s after the mode left auto", h)
		}
	}
	if set, _ := verdict(tt); set {
		t.Fatal("verdict stored outside auto mode")
	}
}

func TestVerdictEventOnlyOnChange(t *testing.T) {
	tt, _, _ := refTunnel(t, testReferences, testCanaries[0])
	count := func(want string) int {
		n := 0
		for _, e := range tt.log.last(100) {
			if strings.Contains(e.Msg, want) {
				n++
			}
		}
		return n
	}
	tt.check()
	waitPass(t, tt)
	tt.check()
	tt.check()
	if n := count("reference verdict: limited=true"); n != 1 {
		t.Fatalf("limited events: %d", n)
	}
	tt.dropVerdict()
	tt.check()
	waitPass(t, tt)
	if n := count("reference verdict"); n != 1 {
		t.Fatalf("same verdict logged again: %d", n)
	}
	probe, _ := probeLog(testCanaries[0], testReferences[0])
	tt.probeTLS = probe
	tt.dropVerdict()
	tt.check()
	waitPass(t, tt)
	if n := count("reference verdict: limited=false"); n != 1 {
		t.Fatalf("open events: %d", n)
	}
}

func trustedSub(t *testing.T, id byte, base Subscription) Subscription {
	s := base
	s.ID = strings.Repeat(string("0123456789abcdef"[id]), 32)
	s.Sources = []string{"https://example.org/sub" + s.ID[:1]}
	return s
}

func TestClearMarkerEndsWhenAnotherListArrives(t *testing.T) {
	subA, _, bundle := subFixture(t)
	trustSubscription(t, subA)
	subB := trustedSub(t, 7, subA)
	cfg := Config{Subscriptions: []Subscription{subA, subB}}
	bundleB := bundle
	bundleB.Sub = bytes.Repeat([]byte{0x77}, 16)
	bundleB.References = []string{}
	cfg, err := ApplySubFile(cfg, cfg.Subscriptions[1], bundleB)
	if err != nil {
		t.Fatal(err)
	}
	bundle.References = []string{"ref1.example.org:443"}
	cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
	if err != nil || len(cfg.References) != 1 {
		t.Fatal(cfg.References, err)
	}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := RestoreSubscriptionState(Config{Subscriptions: []Subscription{subA, subB}}, path)
	if err != nil || len(got.References) != 1 {
		t.Fatalf("another subscription's clear wiped the list: %v %v", got.References, err)
	}
}

func TestClearMarkerEndsWhenStaticBundleCarriesList(t *testing.T) {
	sub, key, bundle := subFixture(t)
	trustSubscription(t, sub)
	bundle.References = []string{}
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil || !cfg.Subscriptions[0].RefsCleared {
		t.Fatal("not cleared", err)
	}
	static := links.Bundle{V: 2, Kind: "routes", Mode: "merge", Routes: []links.Route{subRoute("two")}, References: []string{"ref2.example.org:443"}}
	raw, err := links.Encode(static, key)
	if err != nil {
		t.Fatal(err)
	}
	uri, _ := links.URI(raw, "")
	res, err := ImportLink(cfg, uri, false)
	if err != nil || len(res.Config.References) != 1 {
		t.Fatal(res.Config.References, err)
	}
	cfg2 := res.Config
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, cfg2); err != nil {
		t.Fatal(err)
	}
	got, err := RestoreSubscriptionState(Config{Subscriptions: cfg2.Subscriptions, References: cfg2.References}, path)
	if err != nil || len(got.References) != 1 {
		t.Fatalf("the old clear wiped the pasted list: %v %v", got.References, err)
	}
}

func TestSubscribeLinkKeepsClearMarker(t *testing.T) {
	sub, key, bundle := subFixture(t)
	trustSubscription(t, sub)
	bundle.References = []string{}
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	cfg.References = []string{"ref1.example.org:443"} // a static link's list
	res, err := SubscribeLink(cfg, subInvite(t, sub, key, nil), false)
	if err != nil || !res.Config.Subscriptions[0].RefsCleared {
		t.Fatal("marker lost on re-import", err)
	}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, res.Config); err != nil {
		t.Fatal(err)
	}
	got, err := RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub}, References: cfg.References}, path)
	if err != nil || len(got.References) != 0 {
		t.Fatalf("clear lost after restore: %v %v", got.References, err)
	}
}
