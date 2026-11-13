package tunnel

import (
	"testing"
	"time"
)

func TestHealth(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	conn := func(outcome, route string, d time.Duration) GroupStatus {
		return GroupStatus{LastConn: ago(d), LastConnResult: &connectionResult{Outcome: outcome, Route: route}}
	}
	cases := []struct {
		name   string
		s      Status
		state  string
		reason string
	}{
		{"offline", Status{State: "offline", Started: now.Add(-time.Hour)}, "problem", "no_internet"},
		{"real connection", Status{Started: now.Add(-time.Hour), Groups: map[string]GroupStatus{"a": conn("ok", "direct", time.Minute)}}, "working", ""},
		{"old connection is no evidence", Status{Started: now.Add(-time.Hour), Groups: map[string]GroupStatus{"a": conn("ok", "direct", time.Hour)}}, "ready", "waiting_app"},
		{"reserve only", Status{Started: now.Add(-time.Hour), Tunnels: []RouteState{{ID: "r1", Use: "reserve"}},
			Groups: map[string]GroupStatus{"a": conn("ok", "r1", time.Minute)}}, "limited", "reserve"},
		{"reserve with exit", Status{Started: now.Add(-time.Hour), Tunnels: []RouteState{{ID: "r1", Exit: "a", Use: "reserve"}},
			Groups: map[string]GroupStatus{"a": conn("ok", "r1 (exit=a)", time.Minute)}}, "limited", "reserve"},
		{"reserve and normal", Status{Started: now.Add(-time.Hour), Tunnels: []RouteState{{ID: "r1", Use: "reserve"}},
			Groups: map[string]GroupStatus{"a": conn("ok", "r1", time.Minute), "b": conn("ok", "direct", time.Minute)}}, "working", ""},
		{"probe ok", Status{Started: now.Add(-time.Hour), Groups: map[string]GroupStatus{
			"a": {LastProbe: *ago(time.Minute), LastResult: probeResult{RelayOK: 1}}}}, "working", ""},
		{"probe past quiet interval", Status{Started: now.Add(-time.Hour), Groups: map[string]GroupStatus{
			"a": {LastProbe: *ago(5*time.Minute + time.Second), LastResult: probeResult{RelayOK: 1}}}}, "working", ""},
		{"quiet probe past ten minutes", Status{Started: now.Add(-time.Hour), Groups: map[string]GroupStatus{
			"a": {LastProbe: *ago(11 * time.Minute), LastResult: probeResult{RelayOK: 1}}}}, "working", ""},
		{"nothing used during warmup", Status{Started: now.Add(-30 * time.Second)}, "ready", "waiting_app"},
		{"nothing used yet", Status{Started: now.Add(-100 * time.Second)}, "ready", "waiting_app"},
		{"old failed dials", Status{Started: now.Add(-time.Hour), Mode: ModeAlways,
			Stats: map[string]routeStats{"tunnel": {Fails: 3}}, Groups: map[string]GroupStatus{
				"a": conn("refused", "r1", 6*time.Minute)}}, "problem", "slow"},
		{"relays down", Status{Started: now.Add(-time.Hour), State: "relays-down", Groups: map[string]GroupStatus{
			"a": {LastProbe: *ago(time.Minute)}}}, "problem", "server_down"},
		{"no route", Status{Started: now.Add(-time.Hour), State: "blocked", RouteCount: 0, Groups: map[string]GroupStatus{
			"a": {LastProbe: *ago(time.Minute)}}}, "problem", "no_route"},
		{"all stalled", Status{Started: now.Add(-200 * time.Second), RouteCount: 2, Tunnels: []RouteState{{ID: "x", State: routeIdle}},
			Groups: map[string]GroupStatus{"a": conn("stall", "direct", time.Minute)}}, "problem", "no_route"},
		{"stalls outrank probes", Status{Started: now.Add(-time.Hour), Groups: map[string]GroupStatus{
			"a": conn("stall", "direct", time.Minute), "b": {LastProbe: *ago(time.Minute), LastResult: probeResult{RelayOK: 1}}}}, "problem", "no_route"},
		{"stalls outrank tunnel up", Status{Started: now.Add(-time.Hour), Tunnels: []RouteState{{ID: "r1", State: routeUp}},
			Stats: map[string]routeStats{"tunnel": {Conns: 2}}, Groups: map[string]GroupStatus{"a": conn("stall", "r1", time.Minute)}}, "problem", "no_route"},
		{"later stalls outrank probes", Status{Started: now.Add(-time.Hour), Groups: map[string]GroupStatus{
			"a": conn("later", "direct", time.Minute), "b": {LastProbe: *ago(time.Minute), LastResult: probeResult{RelayOK: 1}}}}, "problem", "no_route"},
		{"stalls too early", Status{Started: now.Add(-100 * time.Second), Groups: map[string]GroupStatus{
			"a": conn("stall", "direct", time.Minute)}}, "starting", ""},
		{"slow", Status{Started: now.Add(-time.Hour), Groups: map[string]GroupStatus{
			"a": {LastProbe: *ago(time.Minute)}}}, "problem", "slow"},
	}
	for _, c := range cases {
		got := health(c.s, now)
		if got.State != c.state || got.Reason != c.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, got.State, got.Reason, c.state, c.reason)
		}
	}
}

func TestHealthKeepsWorkingForTenSeconds(t *testing.T) {
	tun := &Tunnel{}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tun.mu.Lock()
	defer tun.mu.Unlock()
	h := tun.settleHealthLocked(Health{State: "working"}, now)
	if h.State != "working" || !h.Since.Equal(now) {
		t.Fatalf("first verdict %+v", h)
	}
	if h := tun.settleHealthLocked(Health{State: "problem", Reason: "slow"}, now.Add(5*time.Second)); h.State != "working" {
		t.Fatalf("a problem 5 s after working should wait, got %+v", h)
	}
	h = tun.settleHealthLocked(Health{State: "problem", Reason: "slow"}, now.Add(11*time.Second))
	if h.State != "problem" || !h.Since.Equal(now.Add(11*time.Second)) {
		t.Fatalf("after 10 s the problem shows with its own time, got %+v", h)
	}
	if h := tun.settleHealthLocked(Health{State: "working"}, now.Add(12*time.Second)); h.State != "working" {
		t.Fatalf("recovery shows at once, got %+v", h)
	}
}

func TestHealthWithIdleOpenConnection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		kind  groupKind
		route Route
	}{
		{"off", ModeOff, kindRelay, RouteDirect},
		{"other", ModeAuto, kindOther, RouteDirect},
		{"idle tunnel", ModeAuto, kindRelay, RouteTunnel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			tt, g := evidenceTunnel(&now)
			tt.mode = tc.mode
			tt.started = now.Add(-time.Hour)
			if tc.kind == kindOther {
				delete(tt.relays, g.name)
				g = tt.other
			}
			g.route = tc.route
			tt.recordConnection(g, "ok", tc.route.String())
			tt.stats[tc.route].Conns = 1
			tt.conns = map[*tracked]struct{}{{group: g, route: tc.route}: {}}
			now = now.Add(30 * time.Minute)
			if tc.route == RouteTunnel {
				g.state = "blocked"
				g.lastProbe = now
				g.lastResult = probeResult{Relays: 1, NetworkOK: true}
			}
			if h := tt.Status(0).Health; h.State != "working" {
				t.Fatalf("health=%+v", h)
			}
		})
	}
}

func TestHealthSmoothsWorkingToStarting(t *testing.T) {
	tt := &Tunnel{}
	now := time.Unix(1000, 0)
	tt.settleHealthLocked(Health{State: "working"}, now)
	if h := tt.settleHealthLocked(Health{State: "starting"}, now.Add(9*time.Second)); h.State != "working" {
		t.Fatal(h)
	}
	if h := tt.settleHealthLocked(Health{State: "starting"}, now.Add(10*time.Second)); h.State != "starting" {
		t.Fatal(h)
	}
}

func TestStatusDoesNotSettleHealth(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	tt.started = now.Add(-time.Minute)
	tt.recordConnection(g, "ok", "direct")
	tt.settleHealth()
	saved, since, good := tt.health, tt.healthSince, tt.healthGood
	now = now.Add(5 * time.Second)
	for range 3 {
		if h := tt.Status(0).Health; h.State != "working" {
			t.Fatal(h)
		}
	}
	if tt.health != saved || tt.healthSince != since || tt.healthGood != good {
		t.Fatal("Status mutated health")
	}
	tt.settleHealth()
	if !tt.healthGood.Equal(now) {
		t.Fatal("explicit settle did not advance health")
	}
}
