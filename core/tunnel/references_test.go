package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

var testReferences = []string{"ref1.example.org:443", "ref2.example.org:443"}

func TestBundleReferencesPersistAbsentKeepsEmptyClears(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	bundle.References = []string{"example.org:443", "example.net:443"}
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil || !slices.Equal(cfg.References, bundle.References) || !slices.Equal(cfg.Subscriptions[0].References, bundle.References) {
		t.Fatal("references not applied", err)
	}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub}}, path)
	if err != nil || !slices.Equal(cfg.References, bundle.References) {
		t.Fatal("references not restored", err)
	}
	want := slices.Clone(cfg.References)
	// A bundle without the key keeps the list.
	bundle.Revision++
	bundle.References = nil
	cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
	if err != nil || !slices.Equal(cfg.References, want) || !slices.Equal(cfg.Subscriptions[0].References, want) {
		t.Fatal("absent references changed the list", err)
	}
	// An explicit empty list clears it, and the cleared state survives a restart.
	bundle.Revision++
	bundle.References = []string{}
	cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
	if err != nil || len(cfg.References) != 0 || len(cfg.Subscriptions[0].References) != 0 {
		t.Fatal("empty references did not clear", err)
	}
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub}}, path)
	if err != nil || len(cfg.References) != 0 || len(cfg.Subscriptions[0].References) != 0 {
		t.Fatal("cleared references came back after restore", err)
	}
	// A later list sets them again.
	bundle.Revision++
	bundle.References = want
	cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
	if err != nil || !slices.Equal(cfg.References, want) {
		t.Fatal("references not set again", err)
	}
}

func TestEmptyReferencesNeedTrustedAuthor(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	bundle.References = testReferences
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil || len(cfg.References) != 2 {
		t.Fatal(err)
	}
	// The signer stops being the trusted author: its empty list is ignored.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	old := AuthorKey
	AuthorKey = base64.RawURLEncoding.EncodeToString(otherPub)
	defer func() { AuthorKey = old }()
	bundle.Revision++
	bundle.References = []string{}
	cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
	if err != nil || len(cfg.References) != 2 || len(cfg.Subscriptions[0].References) != 2 {
		t.Fatal("untrusted empty list cleared references", err)
	}
}

func TestImportEmptyReferences(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	old := AuthorKey
	AuthorKey = base64.RawURLEncoding.EncodeToString(pub)
	defer func() { AuthorKey = old }()
	initial := Config{Routes: []links.Route{subRoute("zero")}, References: slices.Clone(testReferences)}
	for _, tc := range []struct {
		name   string
		signer ed25519.PrivateKey
		refs   []string
		want   []string
	}{
		{"absent keeps", key, nil, testReferences},
		{"empty clears", key, []string{}, nil},
		{"list replaces", key, []string{"example.net:443"}, []string{"example.net:443"}},
		{"untrusted empty ignored", other, []string{}, testReferences},
		{"unsigned empty ignored", nil, []string{}, testReferences},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := links.Bundle{V: 2, Kind: "routes", Mode: "merge", Routes: []links.Route{subRoute("one")}, References: tc.refs}
			raw, err := links.Encode(bundle, tc.signer)
			if err != nil {
				t.Fatal(err)
			}
			uri, err := links.URI(raw, "")
			if err != nil {
				t.Fatal(err)
			}
			result, err := ImportLink(initial, uri, false)
			if err != nil || !slices.Equal(result.Config.References, tc.want) {
				t.Fatalf("references %v, want %v (%v)", result.Config.References, tc.want, err)
			}
		})
	}
}

// After a clear the router runs no reference probe and never reports the mode.
func TestClearedReferencesRunNoProbe(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	bundle.References = testReferences
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Revision++
	bundle.References = []string{}
	if cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle); err != nil {
		t.Fatal(err)
	}
	tt, g, seen := refTunnel(t, cfg.References, testCanaries[0])
	tt.check()
	waitPass(t, tt)
	tt.check()
	if n := countRefs(seen(), testReferences); n != 0 {
		t.Fatalf("%d reference dials after a clear", n)
	}
	if set, _ := verdict(tt); set {
		t.Fatal("verdict set")
	}
	s := tt.Status(0)
	if s.NetworkLimited || s.Groups[g.name].LastResult.NetworkLimited || s.Groups[g.name].LastResult.ReferenceChecked {
		t.Fatalf("flag set after a clear: %+v", s.Groups[g.name].LastResult)
	}
}

func TestBundleReferencesNeedTrustedAuthor(t *testing.T) {
	sub, _, bundle := subFixture(t)
	bundle.References = []string{"example.org:443"}
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil || len(cfg.References) != 0 {
		t.Fatal("untrusted references applied", err)
	}
}

func TestReferencesNormalize(t *testing.T) {
	cfg, err := Config{Zone: "t.example.org", Key: testKey}.Normalize()
	if err != nil || len(cfg.References) != 0 {
		t.Fatalf("references must have no default: %v %v", cfg.References, err)
	}
	if _, err = (Config{Zone: "t.example.org", Key: testKey, References: slices.Repeat([]string{"example.org:443"}, 9)}).Normalize(); err == nil {
		t.Fatal("nine references accepted")
	}
}

// refTunnel is a tunnel with one blocked relay group. open lists the hosts that answer.
func refTunnel(t *testing.T, refs []string, open ...string) (*Tunnel, *group, func() []string) {
	t.Helper()
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	tt.mu.Lock()
	tt.cfg.References = refs
	tt.mu.Unlock()
	g := tt.testGroup("relay.example.org:443")
	probe, seen := probeLog(open...)
	tt.probeTLS = probe
	return tt, g, seen
}

func countRefs(seen []string, refs []string) int {
	n := 0
	for _, h := range seen {
		if slices.Contains(refs, h) {
			n++
		}
	}
	return n
}

// waitPass waits until no reference pass is in flight.
func waitPass(t *testing.T, tt *Tunnel) {
	t.Helper()
	for range 500 {
		tt.mu.Lock()
		running := tt.refRunning
		tt.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("reference pass did not finish")
}

func verdict(tt *Tunnel) (set, limited bool) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return tt.refSet, tt.refLimited
}

func TestReferenceProbe(t *testing.T) {
	for _, tc := range []struct {
		name     string
		refs     []string
		open     []string
		wantDial int
		limited  bool
		noPass   bool
	}{
		{"first reference answers", testReferences, []string{testCanaries[0], testReferences[0]}, 1, false, false},
		{"second reference answers", testReferences, []string{testCanaries[0], testReferences[1]}, 2, false, false},
		{"all silent", testReferences, testCanaries[:1], 2, true, false},
		{"canary silent", testReferences, nil, 0, false, true},
		{"direct target answers", testReferences, []string{"relay.example.org:443"}, 0, false, true},
		{"no references", nil, testCanaries[:1], 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt, g, seen := refTunnel(t, tc.refs, tc.open...)
			r := tt.probe()[g]
			if r.ReferenceChecked || r.NetworkLimited {
				t.Fatalf("the first probe must not carry a verdict: %+v", r)
			}
			waitPass(t, tt)
			if n := countRefs(seen(), testReferences); n != tc.wantDial {
				t.Fatalf("reference dials: %d, want %d", n, tc.wantDial)
			}
			if set, limited := verdict(tt); set == tc.noPass || (set && limited != tc.limited) {
				t.Fatalf("verdict set=%v limited=%v", set, limited)
			}
		})
	}
}

func TestReferenceVerdictCached(t *testing.T) {
	tt, g, seen := refTunnel(t, testReferences, testCanaries[0], testReferences[0])
	now := time.Now()
	tt.now = func() time.Time { return now }
	tt.probe()
	waitPass(t, tt)
	before := countRefs(seen(), testReferences)
	if before != 1 {
		t.Fatalf("first pass dials: %d, want 1", before)
	}
	// Second pass inside the window: no dials, the cached verdict is used.
	r := tt.probe()[g]
	waitPass(t, tt)
	if n := countRefs(seen(), testReferences); n != before {
		t.Fatalf("cached pass dialled %d times", n-before)
	}
	if !r.ReferenceChecked || r.NetworkLimited {
		t.Fatalf("cached result: %+v", r)
	}
	// Past the window: one more handshake.
	now = now.Add(refFresh + time.Second)
	tt.probe()
	waitPass(t, tt)
	if n := countRefs(seen(), testReferences); n != before+1 {
		t.Fatalf("stale pass dials: %d", n-before)
	}
}

func TestReferenceLastAnsweringFirst(t *testing.T) {
	tt, _, seen := refTunnel(t, testReferences, testCanaries[0], testReferences[1])
	now := time.Now()
	tt.now = func() time.Time { return now }
	tt.probe()
	waitPass(t, tt)
	now = now.Add(refFresh + time.Second)
	tt.probe()
	waitPass(t, tt)
	var refs []string
	for _, h := range seen() {
		if slices.Contains(testReferences, h) {
			refs = append(refs, h)
		}
	}
	if want := []string{testReferences[0], testReferences[1], testReferences[1]}; !slices.Equal(refs, want) {
		t.Fatalf("order %v, want %v", refs, want)
	}
}

func TestReferenceVerdictInvalidated(t *testing.T) {
	for name, drop := range map[string]func(*Tunnel){
		"network changed": func(tt *Tunnel) { tt.NetworkChanged() },
		"mode change":     func(tt *Tunnel) { _ = tt.SetMode(ModeOff) },
	} {
		t.Run(name, func(t *testing.T) {
			tt, _, seen := refTunnel(t, testReferences, testCanaries[0])
			tt.probe()
			waitPass(t, tt)
			if set, limited := verdict(tt); !set || !limited {
				t.Fatal("no limited verdict")
			}
			drop(tt)
			if set, _ := verdict(tt); set {
				t.Fatal("verdict kept")
			}
			if tt.Status(0).NetworkLimited {
				t.Fatal("flag kept")
			}
			_ = seen
		})
	}
	t.Run("canary flips", func(t *testing.T) {
		tt, g, _ := refTunnel(t, testReferences, testCanaries[0])
		tt.probe()
		waitPass(t, tt)
		probe, _ := probeLog()
		tt.probeTLS = probe
		tt.probe()
		if set, _ := verdict(tt); set {
			t.Fatal("verdict kept after the canaries went silent")
		}
		_ = g
	})
}

// blockingRefs makes the relay fail and the canary answer; reference dials
// block until released or cancelled, and report their start and end.
func blockingRefs(tt *Tunnel) (started chan string, ended chan string, release chan struct{}) {
	started, ended, release = make(chan string, 16), make(chan string, 16), make(chan struct{})
	tt.probeTLS = func(ctx context.Context, hp, _ string) error {
		if hp == testCanaries[0] {
			return nil
		}
		if !slices.Contains(testReferences, hp) {
			return errors.New("blocked")
		}
		started <- hp
		defer func() { ended <- hp }()
		select {
		case <-release:
		case <-ctx.Done():
		}
		return errors.New("silent")
	}
	return
}

func recv(t *testing.T, ch chan string, what string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: " + what)
	}
	return ""
}

func TestReferencePassDroppedMidFlight(t *testing.T) {
	tt, _, _ := refTunnel(t, testReferences, testCanaries[0])
	started, _, release := blockingRefs(tt)
	tt.probe()
	recv(t, started, "pass did not start")
	tt.dropVerdict() // drops the pass in flight
	close(release)
	waitPass(t, tt)
	time.Sleep(50 * time.Millisecond)
	if set, _ := verdict(tt); set {
		t.Fatal("a dropped pass stored its verdict")
	}
}

// A dropped or superseded pass stops dialling and frees the way for a new one.
func TestReferencePassCancelledOnDrop(t *testing.T) {
	for name, drop := range map[string]func(*Tunnel){
		"verdict dropped": func(tt *Tunnel) { tt.dropVerdict() },
		"mode change":     func(tt *Tunnel) { _ = tt.SetMode(ModeOff) },
	} {
		t.Run(name, func(t *testing.T) {
			tt, _, _ := refTunnel(t, testReferences, testCanaries[0])
			started, ended, _ := blockingRefs(tt)
			tt.probe()
			first := recv(t, started, "pass did not start")
			drop(tt)
			if got := recv(t, ended, "dial kept running after the drop"); got != first {
				t.Fatalf("ended %s, want %s", got, first)
			}
			select {
			case h := <-started:
				t.Fatalf("a dropped pass went on to dial %s", h)
			case <-time.After(100 * time.Millisecond):
			}
			if name == "mode change" {
				return
			}
			tt.probe() // a fresh pass can start at once
			recv(t, started, "no fresh pass after the drop")
		})
	}
}

// The reference pass runs in the background and never overlaps itself.
func TestReferencePassOffCriticalPathAndSingle(t *testing.T) {
	tt, g, _ := refTunnel(t, testReferences, testCanaries[0])
	var mu sync.Mutex
	dials := 0
	release := make(chan struct{})
	tt.probeTLS = func(ctx context.Context, hp, _ string) error {
		if hp == testCanaries[0] {
			return nil
		}
		if slices.Contains(testReferences, hp) {
			mu.Lock()
			dials++
			mu.Unlock()
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return errors.New("silent")
	}
	done := make(chan struct{})
	go func() {
		tt.probe()
		tt.probe() // a second trigger while the first pass hangs
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("probe waited for the reference pass")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	n := dials
	mu.Unlock()
	if n != 1 {
		t.Fatalf("overlapping passes: %d dials while the first hangs", n)
	}
	if tt.Status(0).NetworkLimited {
		t.Fatal("limited before the pass finished")
	}
	close(release)
	waitPass(t, tt)
	if set, limited := verdict(tt); !set || !limited {
		t.Fatal("no verdict after the pass")
	}
	_ = g
}

func TestReferencePassShutdownIsUnknown(t *testing.T) {
	tt, _, _ := refTunnel(t, testReferences, testCanaries[0])
	tt.probeTLS = func(ctx context.Context, hp, _ string) error {
		if hp == testCanaries[0] {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}
	tt.probe()
	tt.cancel()
	waitPass(t, tt)
	if set, _ := verdict(tt); set {
		t.Fatal("shutdown recorded a verdict")
	}
}

func TestReferencesBounded(t *testing.T) {
	var refs []string
	for i := range 12 {
		refs = append(refs, "ref"+string(rune('a'+i))+".example.org:443")
	}
	tt, _, seen := refTunnel(t, refs, testCanaries[0])
	tt.probe()
	waitPass(t, tt)
	if n := countRefs(seen(), refs); n != 8 {
		t.Fatalf("reference dials: %d, want 8", n)
	}
}

func TestStatusNetworkLimited(t *testing.T) {
	tt, g, _ := refTunnel(t, testReferences, testCanaries[0])
	now := time.Now()
	tt.now = func() time.Time { return now }
	tt.check()
	waitPass(t, tt)
	tt.check() // the second pass carries the cached verdict
	s := tt.Status(0)
	if !s.NetworkLimited || !s.Groups[g.name].LastResult.NetworkLimited {
		t.Fatalf("status: %+v", s.Groups[g.name].LastResult)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["network_limited"] != true {
		t.Fatal("status JSON lacks network_limited")
	}
	if lr, _ := got["last_result"].(map[string]any); lr["network_limited"] != true || lr["reference_checked"] != true {
		t.Fatalf("last_result: %v", lr)
	}
	// The old verdict stays visible for the grace period while a new pass runs.
	now = now.Add(refFresh + time.Second)
	if !tt.Status(0).NetworkLimited {
		t.Fatal("flag dropped while the verdict is re-checked")
	}
	// Time passes beyond the grace period: the flag clears.
	now = now.Add(refGrace)
	if tt.Status(0).NetworkLimited {
		t.Fatal("stale verdict still shown")
	}
	now = now.Add(-refFresh - refGrace)
	if !tt.Status(0).NetworkLimited {
		t.Fatal("fresh verdict hidden")
	}
	// A manual route clears it.
	if err := tt.SetMode(ModeOff); err != nil || tt.Status(0).NetworkLimited {
		t.Fatalf("manual mode keeps the flag: %v", err)
	}
	// Back in auto, a new pass in which a reference answers shows no flag.
	if err := tt.SetMode(ModeAuto); err != nil {
		t.Fatal(err)
	}
	probe, _ := probeLog(testCanaries[0], testReferences[0])
	tt.probeTLS = probe
	tt.check()
	waitPass(t, tt)
	tt.check()
	if tt.Status(0).NetworkLimited {
		t.Fatal("flag not cleared")
	}
}

func TestNetworkChangedDropsFlag(t *testing.T) {
	tt, _, _ := refTunnel(t, testReferences, testCanaries[0])
	tt.check()
	waitPass(t, tt)
	if !tt.Status(0).NetworkLimited {
		t.Fatal("flag not set")
	}
	tt.NetworkChanged()
	if tt.Status(0).NetworkLimited {
		t.Fatal("NetworkChanged kept the flag")
	}
}

func TestNormalizeValidatesTargets(t *testing.T) {
	base := Config{Zone: "t.example.org", Key: testKey}
	for _, tc := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"reference port 0", func(c *Config) { c.References = []string{"example.org:0"} }, "references"},
		{"canary port 0", func(c *Config) { c.Canaries = []string{"example.org:0"} }, "canaries"},
		{"reference space", func(c *Config) { c.References = []string{"exa mple.org:443"} }, "references"},
	} {
		c := base
		tc.edit(&c)
		if _, err := c.Normalize(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	c := base
	c.References = []string{"example.org"}
	if got, err := c.Normalize(); err != nil || got.References[0] != "example.org:443" {
		t.Fatalf("bare host: %v %v", got.References, err)
	}
}

// When every group is skipped as probe-blind nothing was probed, so the
// cached verdict stays.
func TestVerdictKeptWhenNothingProbed(t *testing.T) {
	tt, g, _ := refTunnel(t, testReferences, testCanaries[0])
	now := time.Now()
	tt.now = func() time.Time { return now }
	tt.probe()
	waitPass(t, tt)
	if set, limited := verdict(tt); !set || !limited {
		t.Fatal("no limited verdict")
	}
	tt.mu.Lock()
	g.probeBlind, g.probeBlindAt, g.lastProbe = true, now, now
	tt.mu.Unlock()
	now = now.Add(15 * time.Second)
	tt.probe()
	if set, limited := verdict(tt); !set || !limited {
		t.Fatal("the cached verdict was dropped although nothing was probed")
	}
}

// A clear from the trusted author outlives a restart even when the static
// link carries references; without a clear the link's list is kept.
func TestClearedReferencesBeatLinkAfterRestart(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	link := Config{Subscriptions: []Subscription{sub}, References: slices.Clone(testReferences)}
	bundle.References = []string{"example.net:443"}
	cfg, err := ApplySubFile(link, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	if got, err := RestoreSubscriptionState(link, path); err != nil || !slices.Equal(got.References, []string{"example.net:443"}) {
		t.Fatalf("a subscription list is restored over the link's: %v %v", got.References, err)
	}
	bundle.Revision++
	bundle.References = []string{}
	if cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle); err != nil || len(cfg.References) != 0 {
		t.Fatal("not cleared", err)
	}
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := RestoreSubscriptionState(link, path)
	if err != nil || len(got.References) != 0 {
		t.Fatalf("the link's list came back after a clear: %v %v", got.References, err)
	}
	// A later list sets them again, and the marker goes.
	bundle.Revision++
	bundle.References = testReferences
	if cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle); err != nil || cfg.Subscriptions[0].RefsCleared {
		t.Fatal("marker kept", err)
	}
	// Without any clear the link's list is kept.
	sub2, _, bundle2 := subFixture(t)
	trustSubscription(t, sub2)
	bundle2.References = nil
	cfg2, err := ApplySubFile(Config{Subscriptions: []Subscription{sub2}}, sub2, bundle2)
	if err != nil {
		t.Fatal(err)
	}
	path2 := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path2, cfg2); err != nil {
		t.Fatal(err)
	}
	got, err = RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub2}, References: slices.Clone(testReferences)}, path2)
	if err != nil || !slices.Equal(got.References, testReferences) {
		t.Fatalf("link list lost without a clear: %v %v", got.References, err)
	}
}

func TestSameRevisionIsAppliedAgainAfterUpgrade(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	// A copy stored by an older release: same revision, no schema, no references.
	old := bundle
	old.References = nil
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, old)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Subscriptions[0].Schema = 0
	bundle.References = []string{"example.org:443", "example.net:443"}
	cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
	if err != nil || !slices.Equal(cfg.References, bundle.References) || cfg.Subscriptions[0].Schema != subscriptionSchema {
		t.Fatal("the same revision was not applied again", err)
	}
	// Once applied by this release the same revision is unchanged again.
	if _, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle); !errors.Is(err, links.ErrNotNewer) {
		t.Fatal("same revision applied twice by the current release", err)
	}
	// A lower revision is still refused.
	cfg.Subscriptions[0].Schema = 0
	bundle.Revision--
	if _, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle); !errors.Is(err, links.ErrRollback) {
		t.Fatal("rollback accepted", err)
	}
}

func TestLimitedVerdictHeldWhileRechecked(t *testing.T) {
	tt, g, _ := refTunnel(t, testReferences, testCanaries[0])
	now := time.Now()
	tt.now = func() time.Time { return now }
	tt.probe()
	waitPass(t, tt)
	if r := tt.probe()[g]; !r.ReferenceChecked || !r.NetworkLimited {
		t.Fatalf("fresh limited verdict: %+v", r)
	}
	// Stale: a new pass starts, the probe still carries the old verdict.
	now = now.Add(refFresh + time.Second)
	r := tt.probe()[g]
	if !r.ReferenceChecked || !r.NetworkLimited {
		t.Fatalf("verdict dropped while re-checked: %+v", r)
	}
	waitPass(t, tt)
	// Past the grace period without a new result the verdict is unknown again.
	tt.mu.Lock()
	tt.refAt = now.Add(-refFresh - refGrace - time.Second)
	tt.mu.Unlock()
	if r := tt.probe()[g]; r.ReferenceChecked || r.NetworkLimited {
		t.Fatalf("verdict kept beyond the grace period: %+v", r)
	}
}
