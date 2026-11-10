package tunnel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func holdMemoryFixture(t *testing.T) (*Tunnel, *group, *time.Time) {
	t.Helper()
	resetKnownStrategies()
	now := time.Now().UTC()
	tt := memoryTunnel(Config{StateFile: filepath.Join(t.TempDir(), "state.json"), Operator: "first"}, &now)
	t.Cleanup(resetKnownStrategies)
	g := tt.relayLocked("relay.example.org")
	tt.connectionFeedback(g, RouteDirect, "one", true)
	tt.connectionFeedback(g, RouteDirect, "two", true)
	if g.stalls[RouteDirect].state != Held {
		t.Fatal("fixture did not hold")
	}
	return tt, g, &now
}
func reloadHoldGroup(tt *Tunnel, now *time.Time) (*Tunnel, *group) {
	resetKnownStrategies()
	next := memoryTunnel(tt.cfg, now)
	return next, next.relayLocked("relay.example.org")
}
func TestHoldSurvivesRestart(t *testing.T) {
	tt, g, now := holdMemoryFixture(t)
	until := g.stalls[RouteDirect].until
	next, restored := reloadHoldGroup(tt, now)
	h := restored.stalls[RouteDirect]
	if h == nil || h.state != Held || h.step != 1 || restored.route != RouteTunnel || !h.until.Equal(until) {
		t.Fatalf("restored: %+v route=%v", h, restored.route)
	}
	for range recoveredAfter {
		next.applyProbe(restored, probeResult{Relays: 1, RelayOK: 1})
	}
	if restored.route != RouteTunnel || h.state != Held {
		t.Fatal("probe moved held group")
	}
	*now = until
	next.tick(*now)
	if h.state != Trial || restored.route != RouteTunnel {
		t.Fatal("expiry did not open trial")
	}
	trial := &groupTrial{group: restored, hold: h, route: RouteDirect}
	restored.trial = trial
	next.finishTrial(trial, false)
	_, restored = reloadHoldGroup(next, now)
	if restored.stalls[RouteDirect] != nil || restored.route != RouteDirect {
		t.Fatal("successful trial retained hold")
	}
}
func TestExpiredHoldNotRestored(t *testing.T) {
	for _, age := range []time.Duration{time.Hour, 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			tt, _, now := holdMemoryFixture(t)
			*now = now.Add(age)
			next, g := reloadHoldGroup(tt, now)
			h := g.stalls[RouteDirect]
			if age < 24*time.Hour {
				if h == nil || h.step != 1 || h.state != Open {
					t.Fatalf("step only: %+v", h)
				}
				next.tick(*now)
				if h.state != Open {
					t.Fatal("step history became trial")
				}
				next.connectionFeedback(g, RouteDirect, "three", true)
				next.connectionFeedback(g, RouteDirect, "four", true)
				if h.step != 2 {
					t.Fatal("ladder did not escalate", h.step)
				}
			} else if h != nil {
				t.Fatalf("expired: %+v", h)
			}
		})
	}
}
func TestStepDecays(t *testing.T) {
	tt, g, now := holdMemoryFixture(t)
	*now = g.stalls[RouteDirect].until
	tt, g = reloadHoldGroup(tt, now)
	h := g.stalls[RouteDirect]
	if h == nil || !h.stepOnly || h.step == 0 {
		t.Fatalf("step-only hold=%+v", h)
	}
	*now = h.at.Add(holdMemory)
	tt.tick(*now)
	if h.step != 0 {
		t.Fatal("step did not decay", h.step)
	}
}
func TestProbeBlindSurvivesRestart(t *testing.T) {
	tt, g, now := holdMemoryFixture(t)
	g.probeBlind, g.probeBlindAt = true, *now
	tt.tick(*now)
	next, g := reloadHoldGroup(tt, now)
	if !g.probeBlind {
		t.Fatal("probe blind lost")
	}
	*now = now.Add(24 * time.Hour)
	next.tick(*now)
	if g.probeBlind {
		t.Fatal("probe blind did not expire")
	}
}
func TestHoldNotInheritedByOtherNetwork(t *testing.T) {
	tt, _, now := holdMemoryFixture(t)
	tt.cfg.Operator = "second"
	_, g := reloadHoldGroup(tt, now)
	if g.stalls[RouteDirect] != nil || g.probeBlind {
		t.Fatal("inherited other network hold")
	}
}
func TestPoolRouteHoldSurvivesRestart(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json"), My: "example.org:1080"}
	client, err := newRoutePool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := client.(*routePool)
	defer p.Close()
	for _, addr := range []string{"first.example.org:443", "second.example.org:443"} {
		(&poolConn{pool: p, network: p.network, index: 0, address: addr}).feedback(true)
	}
	resetKnownStrategies()
	client, err = newRoutePool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored := client.(*routePool)
	defer restored.Close()
	h := restored.stalls[networkKey(cfg)][0]
	if h == nil || h.state != Held || h.step != 1 {
		t.Fatalf("pool hold: %+v", h)
	}
	(&poolConn{pool: restored, network: restored.network, index: 0}).feedback(false)
	resetKnownStrategies()
	client, err = newRoutePool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.(*routePool).stalls[networkKey(cfg)][0] != nil {
		t.Fatal("pool reply retained hold")
	}
}
func TestHoldFileHasNoHostNames(t *testing.T) {
	tt, _, now := holdMemoryFixture(t)
	g := tt.newGroup("telegram", kindTelegram, RouteDirect)
	tt.connectionFeedback(g, RouteDirect, "destination.example.org:443", true)
	tt.connectionFeedback(g, RouteDirect, "another.example.org:443", true)
	rememberHold(tt.cfg, "route:primary", savedHold{Until: now.Add(time.Minute), Step: 1, At: *now}, *now)
	flushKnownState(tt.cfg.StateFile)
	data, err := os.ReadFile(tt.cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"relay.example.org", "telegram", "destination.example.org", "another.example.org", `"first"`, `"two"`} {
		if strings.Contains(string(data), private) {
			t.Fatalf("saved private value %q: %s", private, data)
		}
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(data, &state) != nil || len(state["holds"]) == 0 {
		t.Fatal("holds missing")
	}
}
func TestMalformedHoldsIgnored(t *testing.T) {
	for _, bad := range []string{`42`, `[]`, `"unknown"`, `{"bad":true}`, `{"network":{"route:primary":{"until":42}}}`} {
		t.Run(bad, func(t *testing.T) {
			resetKnownStrategies()
			defer resetKnownStrategies()
			now := time.Now().UTC()
			cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json")}
			data := fmt.Sprintf(`{"blocked":{"%s":{"%s":%q}},"holds":%s}`, networkKey(cfg), groupKey("example.org"), now.Format(time.RFC3339Nano), bad)
			if err := os.WriteFile(cfg.StateFile, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if blocked, _ := knownNetworkGroup(cfg, "example.org", now); !blocked {
				t.Fatal("malformed holds broke verdict")
			}
			if h := knownHold(cfg, groupKey("example.org"), now); h != (savedHold{}) {
				t.Fatal(h)
			}
		})
	}
}
func TestClockJumpClampsHold(t *testing.T) {
	for _, jump := range []time.Duration{-365 * 24 * time.Hour, 365 * 24 * time.Hour} {
		t.Run(jump.String(), func(t *testing.T) {
			tt, _, now := holdMemoryFixture(t)
			*now = now.Add(jump)
			_, g := reloadHoldGroup(tt, now)
			if h := g.stalls[RouteDirect]; h != nil && h.until.After(now.Add(30*time.Minute)) {
				t.Fatal("unbounded hold", h.until)
			}
		})
	}
}

func TestIndependentGroupHoldsSurviveRestart(t *testing.T) {
	tt, g, now := holdMemoryFixture(t)
	h := &hold{}
	h.enterHeld(*now)
	h.enterHeld(*now)
	g.stalls[RouteSmart] = h
	tt.tick(*now)
	_, g = reloadHoldGroup(tt, now)
	if g.stalls[RouteDirect] == nil || g.stalls[RouteSmart] == nil || g.stalls[RouteDirect].step != 1 || g.stalls[RouteSmart].step != 2 {
		t.Fatalf("independent holds: %+v", g.stalls)
	}
}

func TestSmartHoldSelectedRouteSurvivesRestart(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	now := time.Now().UTC()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json")}
	tt := memoryTunnel(cfg, &now)
	tt.ways[RouteSmart] = tt.ways[RouteTunnel]
	g := tt.relayLocked("relay.example.org")
	tt.connectionFeedback(g, RouteDirect, "one", true)
	tt.connectionFeedback(g, RouteDirect, "two", true)
	if g.route != RouteSmart {
		t.Fatal("fixture not on smart")
	}
	resetKnownStrategies()
	next := memoryTunnel(cfg, &now)
	next.ways[RouteSmart] = next.ways[RouteTunnel]
	g = next.relayLocked("relay.example.org")
	if g.route != RouteSmart || g.stalls[RouteDirect] == nil || g.stalls[RouteDirect].state != Held {
		t.Fatal("selected smart route lost")
	}
	for range recoveredAfter {
		next.applyProbe(g, probeResult{Relays: 1, RelayOK: 1, SmartOK: true})
	}
	if g.route != RouteSmart {
		t.Fatal("probe moved restored smart hold")
	}
}

func TestHoldMemoryBounds(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	now := time.Now().UTC()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json")}
	for n := range 20 {
		cfg.Operator = fmt.Sprint(n)
		for i := range maxNetworkHolds + 1 {
			at := now.Add(time.Duration(n*100+i) * time.Second)
			rememberHold(cfg, fmt.Sprintf("route:r%d", i), savedHold{Step: 1, At: at}, at)
		}
	}
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	total := 0
	for _, entries := range knownStrategies.holds {
		if len(entries) > maxNetworkHolds {
			t.Fatal("per-network cap exceeded")
		}
		total += len(entries)
	}
	if total != maxSavedHolds {
		t.Fatal("total cap", total)
	}
	cfg.Operator = "0"
	if len(knownStrategies.holds[networkKey(cfg)]) != 0 {
		t.Fatal("oldest entries retained")
	}
	cfg.Operator = "19"
	if _, ok := knownStrategies.holds[networkKey(cfg)]["route:r0"]; ok {
		t.Fatal("oldest entry retained")
	}
}

func TestPoolHoldUsesCurrentNetworkForPersistence(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json"), My: "example.org:1080", Operator: "first"}
	client, err := newRoutePool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	p := client.(*routePool)
	original := cfg
	p.memoryConfig = func() Config { return cfg }
	cfg.Operator = "second"
	for _, addr := range []string{"first.example.org:443", "second.example.org:443"} {
		(&poolConn{pool: p, network: p.network, index: 0, address: addr}).feedback(true)
	}
	if knownHold(original, "route:my", time.Now()) != (savedHold{}) {
		t.Fatal("saved under stale pool network")
	}
	if knownHold(cfg, "route:my", time.Now()).Step != 1 {
		t.Fatal("missing current network hold")
	}
}

func TestMalformedHoldEntryDoesNotDiscardValidEntries(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	now := time.Now().UTC()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json")}
	entries := map[string]any{
		"route:good":            savedHold{Step: 1, At: now, Until: now.Add(time.Minute)},
		"route:bad":             map[string]any{"step": "invalid"},
		"host.example.org":      savedHold{Step: 1, At: now},
		groupKey("example.org"): map[string]any{"route": "unknown", "direct": savedStep{Step: 1, At: now}},
	}
	data, err := json.Marshal(map[string]any{"holds": map[string]any{networkKey(cfg): entries}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.StateFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	if knownHold(cfg, "route:good", now).Step != 1 {
		t.Fatal("valid entry discarded")
	}
	if knownHold(cfg, "route:bad", now) != (savedHold{}) || knownHold(cfg, groupKey("example.org"), now) != (savedHold{}) {
		t.Fatal("malformed entry accepted")
	}
	rememberHold(cfg, "route:good", savedHold{Step: 2, At: now}, now)
	flushKnownState(cfg.StateFile)
	data, err = os.ReadFile(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "host.example.org") || strings.Contains(string(data), "invalid") || strings.Contains(string(data), "unknown") {
		t.Fatal("malformed content retained")
	}
}

func TestPinnedGroupKeepsSavedHoldWithoutChangingRoute(t *testing.T) {
	tt, _, now := holdMemoryFixture(t)
	resetKnownStrategies()
	next := memoryTunnel(tt.cfg, now)
	next.mode = ModeOff
	g := next.newGroup("relay.example.org", kindRelay, RouteDirect)
	if g.stalls[RouteDirect] == nil || g.stalls[RouteDirect].state != Held {
		t.Fatal("group creation lost saved hold")
	}
	next.tickGroupLocked(g, *now)
	if g.route != RouteDirect {
		t.Fatal("restore changed pinned mode route")
	}
	resetKnownStrategies()
	if knownHold(tt.cfg, groupKey(g.name), *now).Direct.Step != 1 {
		t.Fatal("pinned mode erased saved hold")
	}
}
