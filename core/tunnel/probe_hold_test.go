package tunnel

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHeldProbeIsNotRewritten(t *testing.T) {
	now := time.Unix(1000, 0)
	tt := memoryTunnel(Config{}, &now)
	g := newGroup("relay.example.org", kindRelay, RouteDirect)
	tt.connectionFeedback(g, RouteDirect, "a", true)
	tt.connectionFeedback(g, RouteDirect, "b", true)
	raw := probeResult{Relays: 1, RelayOK: 1, NetworkOK: true}
	for n := 1; n <= recoveredAfter+1; n++ {
		tt.applyProbe(g, raw)
		if !reflect.DeepEqual(g.lastResult, raw) || g.route != RouteTunnel || g.okStreak != n || g.state != "direct" {
			t.Fatalf("held probe: result=%+v route=%v ok=%d state=%s", g.lastResult, g.route, g.okStreak, g.state)
		}
	}
	internal := g.lastResult
	internal.DirectHeld = true
	data, err := json.Marshal(internal)
	if err != nil || strings.Contains(string(data), "Held") || strings.Contains(string(data), "held") {
		t.Fatalf("probe JSON: %s, %v", data, err)
	}
	now = g.stalls[RouteDirect].until
	for n := 1; n <= recoveredAfter; n++ {
		tt.applyProbe(g, raw)
		want := RouteTunnel
		if g.route != want || g.stalls[RouteDirect].state != Trial {
			t.Fatalf("probe %d after expiry: route=%v hold=%+v", n, g.route, g.stalls[RouteDirect])
		}
	}
}

func TestRealReplyOutranksFailedProbe(t *testing.T) {
	for _, route := range []Route{RouteDirect, RouteSmart} {
		for _, age := range []time.Duration{-1, 0, 59 * time.Second, 60 * time.Second, 61 * time.Second} {
			t.Run(route.String()+"/"+age.String(), func(t *testing.T) {
				now := time.Unix(1000, 0)
				tt := memoryTunnel(Config{}, &now)
				g := newGroup("relay.example.org", kindRelay, route)
				tt.ways[RouteSmart] = tt.ways[RouteTunnel]
				g.failStreak = blockedAfter - 1
				if age >= 0 {
					tt.connectionFeedback(g, route, "a", false)
					if g.failStreak != 0 {
						t.Fatalf("reply left failure streak %d", g.failStreak)
					}
					now = now.Add(age)
				}
				for range blockedAfter + 1 {
					tt.applyProbe(g, probeResult{Relays: 1, NetworkOK: true, TunnelChecked: true, TunnelOK: true})
				}
				protected := age >= 0 && age < 60*time.Second
				if (g.route == route) != protected {
					t.Fatalf("route=%v protected=%v", g.route, protected)
				}
				if protected && g.failStreak != 0 {
					t.Fatalf("recent reply left failure streak %d", g.failStreak)
				}
			})
		}
	}
}

func TestTunnelReplyClearsTunnelFails(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		t.Run(stringBool(pooled), func(t *testing.T) {
			now := time.Unix(1000, 0)
			tt := memoryTunnel(Config{}, &now)
			if pooled {
				p := &routePool{}
				p.established.Store(true)
				tt.client = p
			}
			g := newGroup("relay.example.org", kindRelay, RouteTunnel)
			g.tunnelFails = 1
			tt.connectionFeedbackVia(g, RouteTunnel, "a", "route-a", false)
			if g.tunnelFails != 0 {
				t.Fatalf("tunnel failures=%d", g.tunnelFails)
			}
			for n := 1; n <= recoveredAfter; n++ {
				tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true})
				want := RouteTunnel
				if n == recoveredAfter {
					want = RouteDirect
				}
				if g.route != want {
					t.Fatalf("probe %d route=%v want=%v", n, g.route, want)
				}
			}
		})
	}
}

func TestCleanMemoryNeedsOpenHold(t *testing.T) {
	for _, state := range []holdState{Open, Suspect, Held, Trial} {
		for _, outcome := range []string{"", "ok", "stall", "later", "refused"} {
			for _, age := range []time.Duration{0, 10*time.Minute - time.Nanosecond, 10 * time.Minute} {
				t.Run([...]string{"open", "suspect", "held", "trial"}[state]+"/"+outcome+"/"+age.String(), func(t *testing.T) {
					resetKnownStrategies()
					defer resetKnownStrategies()
					now := time.Unix(1000, 0)
					tt := memoryTunnel(Config{StateFile: filepath.Join(t.TempDir(), "state.json")}, &now)
					g := newGroup("relay.example.org", kindRelay, RouteDirect)
					if outcome != "" {
						tt.recordConnection(g, outcome, "direct")
					}
					now = now.Add(age)
					tt.recordConnection(g, "ok", "route-a")
					h := &hold{}
					switch state {
					case Suspect:
						h.onStall(now, nil, "", true, "a")
					case Held:
						h.enterHeld(now)
					case Trial:
						h.step = 1
						h.updateState()
					}
					g.stalls[RouteDirect] = h
					for range recoveredAfter {
						tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true})
					}
					_, clean := knownNetworkGroup(tt.cfg, g.name, now)
					want := state == Open && (outcome == "" || outcome == "ok" || age >= 10*time.Minute)
					if clean != want {
						t.Fatalf("clean=%v want=%v", clean, want)
					}
				})
			}
		}
	}
}
