package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func memoryTunnel(cfg Config, now *time.Time) *Tunnel {
	t := &Tunnel{cfg: cfg, mode: ModeAuto, log: newEventLog(30), now: func() time.Time { return *now }, relays: map[string]*group{}}
	t.ways[RouteTunnel] = way{dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unused")
	}}
	return t
}

func TestPreviousNetworkStart(t *testing.T) {
	for _, tc := range []struct {
		name       string
		previous   bool
		here       string
		age        time.Duration
		kind       groupKind
		mode       string
		noState    bool
		noTunnel   bool
		wantTunnel bool
	}{
		{name: "unknown after tunnel", previous: true, wantTunnel: true},
		{name: "unknown after clean"},
		{name: "telegram", previous: true, kind: kindTelegram, wantTunnel: true},
		{name: "no state file", previous: true, noState: true},
		{name: "known clean", previous: true, here: "clean"},
		{name: "known blocked", here: "blocked", wantTunnel: true},
		{name: "expired previous", previous: true, age: previousNetworkMemory + time.Second},
		{name: "previous at boundary", previous: true, age: previousNetworkMemory},
		{name: "other", previous: true, kind: kindOther},
		{name: "off", previous: true, mode: ModeOff},
		{name: "always", previous: true, mode: ModeAlways},
		{name: "selected route", previous: true, mode: "route:test"},
		{name: "no tunnel", previous: true, noTunnel: true},
		{name: "same network", previous: true, here: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetKnownStrategies()
			defer resetKnownStrategies()
			now := time.Now().UTC()
			cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json"), Operator: "previous"}
			current := cfg
			current.Operator = "current"
			if tc.here == "blocked" || tc.here == "clean" {
				rememberBlockedAt(current, "relay.example.org", tc.here == "blocked", now)
			}
			rememberBlockedAt(cfg, "relay.example.org", tc.previous, now.Add(-tc.age))
			if tc.here == "unknown" {
				current = cfg
				knownStrategies.Lock()
				delete(knownStrategies.blocked, networkKey(cfg))
				knownStrategies.Unlock()
			}
			if tc.noState {
				current.StateFile = ""
			}
			tt := memoryTunnel(current, &now)
			if tc.mode != "" {
				tt.mode = tc.mode
			}
			if tc.noTunnel {
				tt.ways[RouteTunnel] = way{}
			}
			g := newGroup("relay.example.org", tc.kind, RouteDirect)
			got := tt.autoRouteLocked(g)
			if (got == RouteTunnel) != tc.wantTunnel {
				t.Fatalf("route %v, want tunnel %v", got, tc.wantTunnel)
			}
			if tc.wantTunnel && (g.state != "blocked" || g.failStreak != blockedAfter) {
				t.Fatalf("inconsistent tunnel state: %s, %d", g.state, g.failStreak)
			}
			if tc.name == "unknown after tunnel" {
				blocked, clean := knownNetworkGroup(current, g.name, now)
				if blocked || clean {
					t.Fatal("inheritance wrote a network verdict")
				}
				if events := tt.log.last(30); len(events) != 1 || events[0].Msg != "relay.example.org: previous network used the tunnel: starting there" {
					t.Fatal("missing inherited start log")
				}
			}
		})
	}
}

func TestInheritedNetworkRecovery(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	now := time.Now().UTC()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json"), Operator: "wifi"}
	rememberBlockedAt(cfg, "relay.example.org", true, now)
	cfg.Operator = "lte"
	tt := memoryTunnel(cfg, &now)
	g := tt.relayLocked("relay.example.org")
	if g.route != RouteTunnel {
		t.Fatal("new network did not inherit tunnel")
	}
	for i := 1; i <= recoveredAfter; i++ {
		now = now.Add(time.Minute)
		tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true})
		blocked, clean := knownNetworkGroup(cfg, g.name, now)
		if i < recoveredAfter && (g.route != RouteTunnel || blocked || clean) {
			t.Fatalf("probe %d left tunnel or assigned a verdict early", i)
		}
	}
	blocked, clean := knownNetworkGroup(cfg, g.name, now)
	if g.route != RouteDirect || blocked || !clean {
		t.Fatalf("recovery: route %v, blocked %v, clean %v", g.route, blocked, clean)
	}
	last := previousNetwork(cfg)
	if last.Network != networkKey(cfg) || last.Groups[groupKey(g.name)] || !last.At.Equal(now) {
		t.Fatalf("last route was not updated: %+v", last)
	}
	resetKnownStrategies()
	if blocked, clean := knownNetworkGroup(cfg, g.name, now); blocked || !clean {
		t.Fatal("clean verdict did not survive reload")
	}
}

func TestInheritedNetworkBlockedProbe(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	now := time.Now().UTC()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json"), Operator: "wifi"}
	rememberBlockedAt(cfg, "relay.example.org", true, now)
	cfg.SystemResolvers = []string{"192.0.2.53:53"}
	tt := memoryTunnel(cfg, &now)
	g := tt.relayLocked("relay.example.org")
	tt.applyProbe(g, probeResult{Relays: 1, TunnelChecked: true, TunnelOK: true})
	if blocked, clean := knownNetworkGroup(cfg, g.name, now); !blocked || clean {
		t.Fatal("blocked probe did not establish a network verdict")
	}
}

func TestNetworkMemorySequence(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	now := time.Now().UTC()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json"), Operator: "wifi"}
	rememberBlockedAt(cfg, "relay.example.org", true, now)
	rememberBlockedAt(cfg, "second.example.org", true, now)
	for i, operator := range []string{"lte", "wifi", "lte"} {
		now = now.Add(time.Hour)
		cfg.Operator = operator
		resetKnownStrategies()
		tt := memoryTunnel(cfg, &now)
		// An unrelated group must not erase the previous network snapshot.
		tt.relayLocked("unseen.example.org")
		for _, name := range []string{"relay.example.org", "second.example.org"} {
			g := tt.relayLocked(name)
			if g.route != RouteTunnel {
				t.Fatalf("step %d (%s), %s: route %v", i, operator, name, g.route)
			}
			tt.applyProbe(g, probeResult{Relays: 1, TunnelOK: true, TunnelChecked: true})
			if g.route != RouteTunnel {
				t.Fatal("probe changed tunnel route")
			}
		}
	}
	// Each network keeps its own verdict after one recovers.
	tt := memoryTunnel(cfg, &now)
	g := tt.relayLocked("relay.example.org")
	for i := 0; i < recoveredAfter; i++ {
		tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true})
	}
	if g.route != RouteDirect {
		t.Fatal("LTE did not recover")
	}
	cfg.Operator = "wifi"
	if blocked, clean := knownNetworkGroup(cfg, "relay.example.org", now); !blocked || clean {
		t.Fatal("LTE recovery changed Wi-Fi memory")
	}
	cfg.Operator = "lte"
	if blocked, clean := knownNetworkGroup(cfg, "relay.example.org", now); blocked || !clean {
		t.Fatal("LTE did not keep its clean memory")
	}
	for _, operator := range []string{"wifi", "lte", "wifi", "lte"} {
		cfg.Operator = operator
		tt := memoryTunnel(cfg, &now)
		g := tt.relayLocked("relay.example.org")
		if (g.route == RouteTunnel) != (operator == "wifi") {
			t.Fatalf("%s lost its own route: %v", operator, g.route)
		}
	}
}

func TestOldNetworkStateLoads(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	now := time.Now().UTC()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json"), Operator: "wifi"}
	data, err := json.Marshal(map[string]any{
		"strategies": map[string]knownStrategy{networkKey(cfg): {Name: "split:2", At: now}},
		"blocked":    map[string]map[string]time.Time{networkKey(cfg): {groupKey("relay.example.org"): now}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.StateFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if blocked, clean := knownNetworkGroup(cfg, "relay.example.org", now); !blocked || clean {
		t.Fatal("old blocked verdict did not load")
	}
	if knownStrategyName(cfg) != "split:2" || !previousNetwork(cfg).At.IsZero() {
		t.Fatal("old strategy or absent last network loaded incorrectly")
	}
}

func TestNetworkVerdictBounds(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	now := time.Now().UTC()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json")}
	for i := 0; i <= maxKnownNetworks; i++ {
		cfg.Operator = fmt.Sprint(i)
		rememberBlockedAt(cfg, "relay.example.org", i%2 == 0, now.Add(time.Duration(i)*time.Minute))
	}
	cfg.Operator = "0"
	if blocked, clean := knownNetworkGroup(cfg, "relay.example.org", now); blocked || clean {
		t.Fatal("oldest network was not evicted")
	}
	for _, blocked := range []bool{false, true} {
		cfg.Operator = fmt.Sprint(blocked)
		rememberBlockedAt(cfg, "relay.example.org", blocked, now)
		if b, c := knownNetworkGroup(cfg, "relay.example.org", now.Add(blockedMemory)); b || c {
			t.Fatal("expired verdict remained known")
		}
	}
}

func TestLastNetworkFollowsRouteChanges(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	now := time.Now().UTC()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json"), Operator: "wifi"}
	tt := memoryTunnel(cfg, &now)
	g := tt.relayLocked("relay.example.org")
	for _, route := range []Route{RouteTunnel, RouteSmart, RouteTunnel, RouteDirect} {
		now = now.Add(time.Minute)
		tt.setRouteLocked(g, route, "test")
		last := previousNetwork(cfg)
		if last.Network != networkKey(cfg) || last.Groups[groupKey(g.name)] != (route == RouteTunnel) || !last.At.Equal(now) {
			t.Fatalf("route %v: last %+v", route, last)
		}
		if blocked, clean := knownNetworkGroup(cfg, g.name, now); blocked || clean {
			t.Fatal("route change without probes wrote a verdict")
		}
	}
}

func TestKnownStateUnchangedDoesNotRewrite(t *testing.T) {
	for _, kind := range []string{"route", "blocked", "clean", "strategy", "forgotten strategy"} {
		t.Run(kind, func(t *testing.T) {
			resetKnownStrategies()
			defer resetKnownStrategies()
			cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json")}
			now := time.Now().UTC()
			remember := func(at time.Time) {
				switch kind {
				case "route":
					rememberTunnel(cfg, "relay.example.org", true, at)
				case "blocked", "clean":
					rememberBlockedAt(cfg, "relay.example.org", kind == "blocked", at)
				case "strategy":
					rememberStrategy(cfg, "split:2")
				case "forgotten strategy":
					rememberStrategy(cfg, "")
				}
			}
			rememberTunnel(cfg, "relay.example.org", true, now)
			remember(now)
			flushKnownState(cfg.StateFile)
			before, err := os.Stat(cfg.StateFile)
			if err != nil {
				t.Fatal(err)
			}
			for _, age := range []time.Duration{time.Second, time.Minute, time.Hour - time.Nanosecond} {
				remember(now.Add(age))
				flushKnownState(cfg.StateFile)
				after, err := os.Stat(cfg.StateFile)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(before, after) {
					t.Fatalf("unchanged %s rewrote state after %v", kind, age)
				}
			}
			if kind == "route" || kind == "blocked" || kind == "clean" {
				remember(now.Add(time.Hour))
				flushKnownState(cfg.StateFile)
				resetKnownStrategies()
				if last := previousNetwork(cfg); !last.At.Equal(now.Add(time.Hour)) {
					t.Fatalf("hourly refresh was not persisted: %+v", last)
				}
			}
		})
	}
}

func TestKnownStateFlushKeepsVerdictThenRouteOrder(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	cfg := Config{StateFile: filepath.Join(t.TempDir(), "state.json")}
	now := time.Now().UTC()
	// Exercise updates while the writer may be saving an earlier snapshot.
	for i := 0; i < 200; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		rememberBlockedAt(cfg, "relay.example.org", true, at)
		rememberTunnel(cfg, "relay.example.org", false, at)
	}
	flushKnownState(cfg.StateFile)
	resetKnownStrategies()
	if blocked, clean := knownNetworkGroup(cfg, "relay.example.org", now); !blocked || clean {
		t.Fatal("flush lost the verdict")
	}
	last := previousNetwork(cfg)
	if last.Network != networkKey(cfg) || last.Groups[groupKey("relay.example.org")] || !last.At.Equal(now.Add(199*time.Second)) {
		t.Fatalf("flush lost the later route update: %+v", last)
	}
	// A route-only change must persist without changing the verdict.
	rememberTunnel(cfg, "relay.example.org", true, now.Add(200*time.Second))
	flushKnownState(cfg.StateFile)
	resetKnownStrategies()
	if !previousNetwork(cfg).Groups[groupKey("relay.example.org")] {
		t.Fatal("changed route did not survive flush")
	}
	if blocked, clean := knownNetworkGroup(cfg, "relay.example.org", now); !blocked || clean {
		t.Fatal("route update changed the verdict")
	}
}

func TestTunnelCloseFlushesKnownState(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	tt := newTestTunnel(t, ModeAuto, nil, nil)
	tt.cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	now := time.Now().UTC()
	rememberBlockedAt(tt.cfg, "relay.example.org", true, now)
	tt.Close()
	data, err := os.ReadFile(tt.cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Blocked map[string]map[string]time.Time `json:"blocked"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Blocked[networkKey(tt.cfg)][groupKey("relay.example.org")].Equal(now) {
		t.Fatal("Close did not persist the pending verdict")
	}
}
