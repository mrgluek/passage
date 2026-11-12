package tunnel

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func trialFixture(t *testing.T) (*Tunnel, *group, *time.Time, *int) {
	t.Helper()
	now := time.Unix(1000, 0)
	tt := memoryTunnel(Config{}, &now)
	tt.ctx = context.Background()
	tt.log = newEventLog(100, tt.timeNow)
	g := tt.newGroup("example.org", kindRelay, RouteTunnel)
	tt.relays[g.name] = g
	calls := 0
	dial := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		t.Cleanup(func() { a.Close(); b.Close() })
		return a, nil
	}
	tt.ways[RouteDirect] = way{dial: func(ctx context.Context, n, a string) (net.Conn, error) { calls++; return dial(ctx, n, a) }, timeout: time.Second}
	tt.ways[RouteTunnel] = way{dial: dial, timeout: time.Second}
	h := &hold{}
	h.enterHeld(now)
	g.stalls[RouteDirect] = h
	now = h.until
	for range recoveredAfter {
		tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1})
	}
	return tt, g, &now, &calls
}

func trialDial(t *testing.T, tt *Tunnel, g *group) *trialConn {
	t.Helper()
	c, r, err := tt.dial(g, "example.org:443")
	if err != nil || r != RouteDirect {
		t.Fatalf("trial dial: %v %v", r, err)
	}
	tc, ok := c.(*trialConn)
	if !ok {
		t.Fatalf("trial connection: %T", c)
	}
	t.Cleanup(func() { c.Close() })
	return tc
}

func TestTrialUsesOneConnection(t *testing.T) {
	tt, g, _, calls := trialFixture(t)
	trialDial(t, tt, g)
	for range 3 {
		c, r, err := tt.dial(g, "example.org:443")
		if err != nil || r != RouteTunnel {
			t.Fatalf("parallel dial: %v %v", r, err)
		}
		c.Close()
	}
	if *calls != 1 {
		t.Fatal(*calls)
	}
}

func TestTrialKeepsTunnelConnections(t *testing.T) {
	tt, g, now, _ := trialFixture(t)
	h := g.stalls[RouteDirect]
	h.state, h.until, g.okStreak = Held, now.Add(2*time.Minute), 0
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	tt.conns = map[*tracked]struct{}{{group: g, route: RouteTunnel, local: a, remote: a}: {}}
	*now = h.until
	for range recoveredAfter {
		tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1})
	}
	if h.state != Trial || g.route != RouteTunnel {
		t.Fatalf("%+v %v", h, g.route)
	}
	tc := trialDial(t, tt, g)
	tc.finish(true)
	a.SetWriteDeadline(time.Now().Add(time.Second))
	go b.Read(make([]byte, 1))
	if _, err := a.Write([]byte{1}); err != nil {
		t.Fatal("tunnel closed", err)
	}
}

func TestTrialOkMovesGroup(t *testing.T) {
	tt, g, _, _ := trialFixture(t)
	trialDial(t, tt, g).finish(false)
	h := g.stalls[RouteDirect]
	if h.state != Open || h.step != 0 || g.route != RouteDirect {
		t.Fatalf("hold=%+v route=%v", h, g.route)
	}
	want := "resumed: " + holdUnit(g, RouteDirect) + " (trial ok)"
	if tt.log.last(1)[0].Msg != want {
		t.Fatal(tt.log.last(1))
	}
}

func TestTrialFailureIsOneStall(t *testing.T) {
	tt, g, now, _ := trialFixture(t)
	tc := trialDial(t, tt, g)
	tc.finish(true)
	tc.finish(true)
	h := g.stalls[RouteDirect]
	if h.state != Held || h.step != 2 || h.count != 1 || h.until != now.Add(10*time.Minute) {
		t.Fatalf("%+v", h)
	}
	want := "stalled: " + holdUnit(g, RouteDirect) + " (trial failed), paused for 10 min, count=1"
	if tt.log.last(1)[0].Msg != want {
		t.Fatal(tt.log.last(1))
	}
}

func TestProbeBlindAfterTwoTrials(t *testing.T) {
	for _, good := range []bool{false, true} {
		t.Run(fmt.Sprint(good), func(t *testing.T) {
			tt, g, now, _ := trialFixture(t)
			trialDial(t, tt, g).finish(true)
			if good {
				tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1})
			}
			*now = g.stalls[RouteDirect].until
			g.okStreak = recoveredAfter
			tt.tick(*now)
			trialDial(t, tt, g).finish(true)
			if g.probeBlind != good {
				t.Fatalf("blind=%v", g.probeBlind)
			}
			if !good {
				return
			}
			if g.probeIn() != 5*time.Minute {
				t.Fatal(g.probeIn())
			}
			want := "disagree: " + g.historyUnit() + " probe=ok conn=stall via direct, 2 trials failed, probes ignored for return"
			count := 0
			for _, e := range tt.log.last(100) {
				if e.Msg == want {
					count++
				}
				if (strings.HasPrefix(e.Msg, "stalled:") || strings.HasPrefix(e.Msg, "resumed:") || strings.HasPrefix(e.Msg, "disagree:")) && strings.Contains(e.Msg, "example.org") {
					t.Fatal(e.Msg)
				}
			}
			if count != 1 {
				t.Fatal(count)
			}
			*now = g.stalls[RouteDirect].until
			g.okStreak = 0
			tt.tick(*now)
			if g.stalls[RouteDirect].state != Trial {
				t.Fatal("blind needs probes")
			}
			trialDial(t, tt, g).finish(false)
			if g.probeBlind {
				t.Fatal("success did not clear blind")
			}
			g.probeBlind = true
			g.probeBlindAt = *now
			*now = now.Add(24*time.Hour - time.Nanosecond)
			tt.tick(*now)
			if !g.probeBlind {
				t.Fatal("blind expired early")
			}
			*now = now.Add(time.Nanosecond)
			tt.tick(*now)
			if g.probeBlind {
				t.Fatal("blind did not expire")
			}
		})
	}
}

func TestProbeOnlyBlockReturnsOnProbes(t *testing.T) {
	now := time.Unix(1000, 0)
	tt := memoryTunnel(Config{}, &now)
	g := tt.newGroup("example.org", kindRelay, RouteDirect)
	for range blockedAfter {
		tt.applyProbe(g, probeResult{Relays: 1, NetworkOK: true, TunnelOK: true})
	}
	if g.route != RouteTunnel {
		t.Fatal(g.route)
	}
	for n := 1; n <= recoveredAfter; n++ {
		tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1})
		if (g.route == RouteDirect) != (n == recoveredAfter) {
			t.Fatal(n, g.route)
		}
	}
}

func TestTrialEligibility(t *testing.T) {
	for _, mode := range []string{ModeAlways, ModeOff, "route:test"} {
		t.Run(mode, func(t *testing.T) {
			tt, g, _, calls := trialFixture(t)
			tt.mode = mode
			c, _, _ := tt.dial(g, "example.org:443")
			if c != nil {
				c.Close()
			}
			if *calls != 0 {
				t.Fatal("pinned trial")
			}
		})
	}
	t.Run("other", func(t *testing.T) {
		tt, g, _, calls := trialFixture(t)
		g.kind = kindOther
		c, _, _ := tt.dial(g, "example.org:443")
		if c != nil {
			c.Close()
		}
		if *calls != 0 {
			t.Fatal("other trial")
		}
	})
	t.Run("spacing", func(t *testing.T) {
		tt, g, now, calls := trialFixture(t)
		trialDial(t, tt, g).Close()
		*now = now.Add(30*time.Second - time.Nanosecond)
		c, r, _ := tt.dial(g, "example.org:443")
		if c != nil {
			c.Close()
		}
		if r != RouteTunnel || *calls != 1 {
			t.Fatal("trial too soon")
		}
		*now = now.Add(time.Nanosecond)
		trialDial(t, tt, g)
		if *calls != 2 {
			t.Fatal(*calls)
		}
	})
}

func TestTrialDialTimeoutFallsThrough(t *testing.T) {
	tt, g, _, _ := trialFixture(t)
	tt.ways[RouteDirect].dial = func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }
	c, r, err := tt.dial(g, "example.org:443")
	if c != nil {
		c.Close()
	}
	if err != nil || r != RouteTunnel || g.stalls[RouteDirect].state != Held || g.stalls[RouteDirect].count != 1 {
		t.Fatalf("%v %v %+v", r, err, g.stalls[RouteDirect])
	}
}

func TestTrialResponseWatch(t *testing.T) {
	for _, reply := range []bool{false, true} {
		t.Run(fmt.Sprint(reply), func(t *testing.T) {
			tt, g, _, _ := trialFixture(t)
			a, b := net.Pipe()
			defer b.Close()
			tt.ways[RouteDirect].dial = func(context.Context, string, string) (net.Conn, error) { return a, nil }
			tc := trialDial(t, tt, g)
			watch := tc.Conn.(*responseWatch)
			if watch.firstLimit != 8*time.Second {
				t.Fatal(watch.firstLimit)
			}
			watch.firstLimit = 20 * time.Millisecond
			peerDone := make(chan struct{})
			go func() {
				defer close(peerDone)
				b.Read(make([]byte, 1))
				if reply {
					b.Write([]byte{1})
				}
			}()
			tc.SetDeadline(time.Now().Add(time.Second))
			if _, err := tc.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			_, err := tc.Read(make([]byte, 1))
			<-peerDone
			tt.mu.Lock()
			defer tt.mu.Unlock()
			h := g.stalls[RouteDirect]
			if reply {
				if err != nil || h.state != Open || g.route != RouteDirect {
					t.Fatalf("%v %+v %v", err, h, g.route)
				}
			} else {
				if err == nil || h.state != Held || h.step != 2 || h.count != 1 || g.route != RouteTunnel {
					t.Fatalf("%v %+v %v", err, h, g.route)
				}
			}
		})
	}
}

func TestTrialWaitsForGoodProbes(t *testing.T) {
	tt, g, now, _ := trialFixture(t)
	h := g.stalls[RouteDirect]
	h.state = Held
	h.until = *now
	g.okStreak = 0
	before := len(tt.log.last(100))
	tt.tick(*now)
	if h.state != Held || len(tt.log.last(100)) != before {
		t.Fatal("hold ended without probes")
	}
	for n := 1; n <= recoveredAfter; n++ {
		tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1})
		want := Held
		if n == recoveredAfter {
			want = Trial
		}
		if h.state != want || g.route != RouteTunnel {
			t.Fatalf("%d %+v %v", n, h, g.route)
		}
	}
	tt.tick(*now)
	tt.tick(*now)
	count := 0
	for _, e := range tt.log.last(100)[before:] {
		if strings.Contains(e.Msg, "cooldown ended, trial next") {
			count++
			if !e.Time.Equal(*now) {
				t.Fatal(e.Time)
			}
		}
	}
	if count != 1 {
		t.Fatal(count)
	}
}

func TestTrialLadderTop(t *testing.T) {
	tt, g, now, _ := trialFixture(t)
	for _, delay := range []time.Duration{10 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		trialDial(t, tt, g).finish(true)
		h := g.stalls[RouteDirect]
		if h.until.Sub(*now) != delay {
			t.Fatal(h.until.Sub(*now))
		}
		*now = h.until
		g.okStreak = recoveredAfter
		tt.tick(*now)
	}
}

func TestTrialRefusalDoesNotHold(t *testing.T) {
	tt, g, _, _ := trialFixture(t)
	tt.ways[RouteDirect].dial = func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused }
	c, r, err := tt.dial(g, "example.org:443")
	if c != nil {
		c.Close()
	}
	if err != nil || r != RouteTunnel || g.stalls[RouteDirect].state != Trial || g.stalls[RouteDirect].count != 0 {
		t.Fatalf("%v %v %+v", r, err, g.stalls[RouteDirect])
	}
}

func TestTrialSmartHold(t *testing.T) {
	tt, g, now, _ := trialFixture(t)
	g.stalls[RouteSmart] = g.stalls[RouteDirect]
	g.stalls[RouteDirect] = nil
	trialDial(t, tt, g).finish(true)
	h := g.stalls[RouteSmart]
	if h.state != Held || h.until != now.Add(10*time.Minute) || g.route != RouteTunnel {
		t.Fatalf("%+v %v", h, g.route)
	}
	*now = h.until
	g.okStreak = recoveredAfter
	tt.tick(*now)
	trialDial(t, tt, g).finish(false)
	if h.state != Open || g.route != RouteDirect {
		t.Fatalf("%+v %v", h, g.route)
	}
}

func TestTrialSuccessClosesTunnelConnections(t *testing.T) {
	tt, g, _, _ := trialFixture(t)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	tt.conns = map[*tracked]struct{}{{group: g, route: RouteTunnel, local: a, remote: a}: {}}
	trialDial(t, tt, g).finish(false)
	if _, err := a.Write([]byte{1}); err == nil {
		t.Fatal("tunnel connection still open")
	}
}

func TestProbeBlindCadenceWithOtherGroup(t *testing.T) {
	now := time.Unix(1000, 0)
	tt := firstProbeTunnel(t)
	tt.now = func() time.Time { return now }
	g := tt.testGroup("example.org:443")
	g.probeBlind = true
	g.probeBlindAt = now
	g.lastProbe = now
	fast := tt.testGroup("second.example.org:443")
	fast.route = RouteTunnel
	var blindCalls atomic.Int32
	tt.probeTLS = func(_ context.Context, h, _ string) error {
		if strings.HasPrefix(h, "example.org:") {
			blindCalls.Add(1)
		}
		return nil
	}
	for range 9 {
		now = now.Add(30 * time.Second)
		tt.check()
	}
	if blindCalls.Load() != 0 {
		t.Fatal(blindCalls.Load())
	}
	now = now.Add(30 * time.Second)
	tt.check()
	if blindCalls.Load() != 1 {
		t.Fatal(blindCalls.Load())
	}
}

func TestTrialCleanMemory(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	tt, g, now, _ := trialFixture(t)
	tt.cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	rememberBlockedAt(tt.cfg, g.name, true, *now)
	trialDial(t, tt, g).finish(true)
	blocked, clean := knownNetworkGroup(tt.cfg, g.name, *now)
	if !blocked || clean {
		t.Fatal("failed trial cleared memory")
	}
	*now = g.stalls[RouteDirect].until
	g.okStreak = recoveredAfter
	tt.tick(*now)
	trialDial(t, tt, g).finish(false)
	blocked, clean = knownNetworkGroup(tt.cfg, g.name, *now)
	if blocked || !clean {
		t.Fatal("successful trial did not save clean memory")
	}
}

func TestTrialPinnedRouteGuard(t *testing.T) {
	tt, g, now, calls := trialFixture(t)
	tt.only = "route:test"
	h := g.stalls[RouteDirect]
	h.state = Held
	h.until = *now
	tt.tick(*now)
	if h.state != Held {
		t.Fatal("pinned hold entered trial")
	}
	h.until = time.Time{}
	h.state = Trial
	c, _, err := tt.dial(g, "example.org:443")
	if c != nil {
		c.Close()
	}
	if err != nil || *calls != 0 {
		t.Fatal("pinned direct trial", err, *calls)
	}
}

func TestSmartHoldExpiresWithoutGoodDirectProbes(t *testing.T) {
	tt, g, now, _ := trialFixture(t)
	g.stalls[RouteDirect] = nil
	g.route = RouteSmart
	tt.ways[RouteSmart] = tt.ways[RouteTunnel]
	tt.connectionFeedback(g, RouteSmart, "first", true)
	tt.connectionFeedback(g, RouteSmart, "second", true)
	h := g.stalls[RouteSmart]
	if h.state != Held || g.route != RouteTunnel {
		t.Fatalf("hold=%+v route=%v", h, g.route)
	}
	probe := probeResult{Relays: 1, NetworkOK: true, SmartOK: true}
	*now = h.until.Add(-time.Nanosecond)
	tt.applyProbe(g, probe)
	if h.state != Held || g.route != RouteTunnel {
		t.Fatalf("hold expired early: hold=%+v route=%v", h, g.route)
	}
	*now = h.until
	tt.applyProbe(g, probe)
	if h.state != Trial || g.route != RouteTunnel || g.okStreak != 0 {
		t.Fatalf("hold=%+v route=%v good direct probes=%d", h, g.route, g.okStreak)
	}
}

func TestDirectTrialFallsBackToSmartWithoutTunnel(t *testing.T) {
	tt, g, now, _ := trialFixture(t)
	tt.ways[RouteSmart], tt.ways[RouteTunnel] = tt.ways[RouteTunnel], way{}
	g.route = RouteSmart

	tc := trialDial(t, tt, g)
	c, route, err := tt.dial(g, "example.org:443")
	if err != nil || route != RouteSmart {
		t.Fatalf("while trial is in flight: route=%v error=%v", route, err)
	}
	c.Close()
	tc.Close()
	*now = now.Add(30 * time.Second)

	tt.ways[RouteDirect].dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, context.DeadlineExceeded
	}
	c, route, err = tt.dial(g, "example.org:443")
	if err != nil || route != RouteSmart {
		t.Fatalf("after trial timed out: route=%v error=%v", route, err)
	}
	c.Close()
	h := g.stalls[RouteDirect]
	if h.state != Held || h.step != 2 || h.until.Sub(*now) != 10*time.Minute {
		t.Fatalf("hold=%+v", h)
	}
	*now = h.until
	g.okStreak = recoveredAfter
	tt.tick(*now)
	tt.ways[RouteDirect] = tt.ways[RouteSmart]
	trialDial(t, tt, g).finish(false)
	if h.state != Open || g.route != RouteDirect {
		t.Fatalf("hold=%+v route=%v", h, g.route)
	}
}

func TestLongHeldGroupKeepsTrial(t *testing.T) {
	tt, g, now, _ := trialFixture(t)
	h := g.stalls[RouteDirect]
	h.enterHeld(*now)
	*now = now.Add(holdMemory + time.Minute)
	g.okStreak = 0
	tt.tick(*now)
	if h.state != Held || h.step != 2 {
		t.Fatalf("long hold=%+v", h)
	}
	for range recoveredAfter {
		tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1})
	}
	tt.tick(*now)
	if h.state != Trial || h.step != 2 || g.route != RouteTunnel {
		t.Fatalf("hold=%+v route=%v", h, g.route)
	}
	trialDial(t, tt, g).finish(true)
	if h.state != Held || h.step != 3 || h.until.Sub(*now) != 30*time.Minute {
		t.Fatalf("hold=%+v", h)
	}
}

func TestInconclusiveTrialReturnsToSameHoldStep(t *testing.T) {
	tt, g, now, _ := trialFixture(t)
	h := g.stalls[RouteDirect]
	tc := trialDial(t, tt, g)
	tc.Close()
	*now = now.Add(30 * time.Second)
	inFlight := trialDial(t, tt, g)

	*now = now.Add(90 * time.Second)
	tt.tick(*now)
	if h.state != Held || h.step != 1 || h.until.Sub(*now) != 2*time.Minute || h.count != 0 {
		t.Fatalf("hold=%+v", h)
	}
	inFlight.finish(false)
	if h.state != Held || g.route != RouteTunnel || g.failedTrials != 0 {
		t.Fatalf("late verdict: hold=%+v route=%v failed trials=%d", h, g.route, g.failedTrials)
	}
	*now = h.until
	tt.tick(*now)
	trialDial(t, tt, g).finish(false)
	if h.state != Open || g.route != RouteDirect {
		t.Fatalf("hold=%+v route=%v", h, g.route)
	}
}

func TestSmartTrialKeepsTunnelUntilFirstReply(t *testing.T) {
	for _, reply := range []bool{true, false} {
		t.Run(map[bool]string{true: "reply", false: "stall"}[reply], func(t *testing.T) {
			tt, g, now, _ := trialFixture(t)
			g.stalls[RouteDirect] = nil
			h := &hold{}
			h.enterHeld(*now)
			g.stalls[RouteSmart] = h
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()
			tt.conns = map[*tracked]struct{}{{group: g, route: RouteTunnel, local: local, remote: local}: {}}
			tt.ways[RouteSmart] = way{timeout: time.Second, dial: func(context.Context, string, string) (net.Conn, error) {
				client, server := net.Pipe()
				t.Cleanup(func() { server.Close() })
				if reply {
					go server.Write([]byte("reply"))
				}
				return client, nil
			}}

			*now = h.until
			tt.applyProbe(g, probeResult{Relays: 1, NetworkOK: true, SmartOK: true})
			if g.route != RouteTunnel || h.state != Trial {
				t.Fatalf("route=%v hold=%+v", g.route, h)
			}
			c, route, err := tt.dial(g, "example.org:443")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			trial, ok := c.(*trialConn)
			if !ok || route != RouteSmart || g.route != RouteTunnel {
				t.Fatalf("trial=%T route=%v group=%v", c, route, g.route)
			}
			if reply {
				if _, err := c.Read(make([]byte, 5)); err != nil {
					t.Fatal(err)
				}
				if h.state != Open || g.route != RouteSmart || local.SetDeadline(time.Now()) == nil {
					t.Fatalf("reply: hold=%+v route=%v", h, g.route)
				}
			} else {
				trial.finish(true)
				if h.state != Held || g.route != RouteTunnel || local.SetDeadline(time.Now().Add(time.Second)) != nil {
					t.Fatalf("stall: hold=%+v route=%v", h, g.route)
				}
			}
		})
	}
}

func TestTrialWindowLogsSkippedOnce(t *testing.T) {
	tt, g, now, calls := trialFixture(t)
	h := g.stalls[RouteDirect]
	tt.tick(*now)
	if h.state != Trial {
		t.Fatalf("state=%v", h.state)
	}
	*now = h.trialUntil
	tt.tick(*now)
	tt.tick(*now)
	count := 0
	for _, event := range tt.log.last(100) {
		if event.Msg == holdUnit(g, RouteDirect)+": trial skipped, no new connection" {
			count++
		}
	}
	if count != 1 || *calls != 0 || h.state != Held || h.step != 1 || h.count != 0 || g.failedTrials != 0 {
		t.Fatalf("events=%v calls=%d hold=%+v failed trials=%d", tt.log.last(100), *calls, h, g.failedTrials)
	}
}
