package tunnel

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func shortRace(t *testing.T) {
	t.Helper()
	h, s, p := hedgeDelay, slowConnect, preferDelay
	hedgeDelay, slowConnect, preferDelay = 100*time.Millisecond, 300*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { hedgeDelay, slowConnect, preferDelay = h, s, p })
}

func pipeDialer(delay time.Duration, err error, calls *atomic.Int32, cancelled *atomic.Bool) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if calls != nil {
			calls.Add(1)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			if cancelled != nil {
				cancelled.Store(true)
			}
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
}

func TestSlowTrackStartsAndEscalates(t *testing.T) {
	now := time.Unix(1000, 0)
	var s slowTrack
	if s.slow(now) != 0 || s.slow(now.Add(time.Second)) != 0 {
		t.Fatal("two slow connects must not start a period")
	}
	if d := s.slow(now.Add(2 * time.Second)); d != 3*time.Minute || !s.active(now.Add(time.Minute)) {
		t.Fatalf("third slow connect started %v", d)
	}
	if s.slow(now.Add(10*time.Second)) != 0 {
		t.Fatal("a slow connect inside a period must not restart it")
	}
	if s.active(now.Add(4 * time.Minute)) {
		t.Fatal("the period must end")
	}
	later := now.Add(5 * time.Minute)
	s.slow(later)
	s.slow(later.Add(time.Second))
	if d := s.slow(later.Add(2 * time.Second)); d != 10*time.Minute {
		t.Fatalf("second period lasted %v, want 10m", d)
	}
	// Long after, the memory is gone.
	far := later.Add(3 * time.Hour)
	s.slow(far)
	s.slow(far.Add(time.Second))
	if d := s.slow(far.Add(2 * time.Second)); d != 3*time.Minute {
		t.Fatalf("after a quiet hour the period lasted %v, want 3m", d)
	}
}

func TestSlowTrackIgnoresOldEventsAndForgetsAfterFastConnects(t *testing.T) {
	now := time.Unix(1000, 0)
	var s slowTrack
	s.slow(now)
	s.slow(now.Add(time.Minute))
	if s.slow(now.Add(6*time.Minute)) != 0 {
		t.Fatal("events older than the window must not count")
	}
	s = slowTrack{step: 2}
	for i := 0; i < fastToForget-1; i++ {
		if s.fastDial(now) {
			t.Fatal("forgot too early")
		}
	}
	if !s.fastDial(now) || s.step != 0 {
		t.Fatal("fast connects in a row must end the memory")
	}
	s.slow(now)
	s.slow(now)
	s.fastDial(now)
	if len(s.events) != 0 {
		t.Fatal("a fast connect must clear the pending slow events")
	}
}

func TestRaceDialFastPrimaryNeverStartsTheSecondary(t *testing.T) {
	shortRace(t)
	var secondary atomic.Int32
	o := raceDial(context.Background(), pipeDialer(0, nil, nil, nil), pipeDialer(0, nil, &secondary, nil), time.Second, time.Second, hedgeDelay, "x:1")
	if o.err != nil || o.winner != 0 || o.hedged {
		t.Fatalf("%+v", o)
	}
	o.conn.Close()
	time.Sleep(2 * hedgeDelay)
	if secondary.Load() != 0 {
		t.Fatal("the secondary started although the primary was fast")
	}
}

func TestRaceDialSecondaryWinsAgainstASlowPrimary(t *testing.T) {
	shortRace(t)
	var cancelled atomic.Bool
	o := raceDial(context.Background(), pipeDialer(2*time.Second, nil, nil, &cancelled), pipeDialer(10*time.Millisecond, nil, nil, nil), 5*time.Second, 5*time.Second, hedgeDelay, "x:1")
	if o.err != nil || o.winner != 1 || !o.hedged {
		t.Fatalf("%+v", o)
	}
	o.conn.Close()
	time.Sleep(50 * time.Millisecond)
	if !cancelled.Load() {
		t.Fatal("the slow primary was not cancelled")
	}
}

func TestRaceDialPrimaryStillWinsWhenItFinishesBeforeTheSecondary(t *testing.T) {
	shortRace(t)
	o := raceDial(context.Background(), pipeDialer(hedgeDelay+20*time.Millisecond, nil, nil, nil), pipeDialer(time.Second, nil, nil, nil), 5*time.Second, 5*time.Second, hedgeDelay, "x:1")
	if o.err != nil || o.winner != 0 || !o.hedged {
		t.Fatalf("%+v", o)
	}
	o.conn.Close()
}

func TestRaceDialFailureBeforeTheDelayLeavesTheFallbackToTheCaller(t *testing.T) {
	shortRace(t)
	var secondary atomic.Int32
	boom := errors.New("refused")
	o := raceDial(context.Background(), pipeDialer(0, boom, nil, nil), pipeDialer(0, nil, &secondary, nil), time.Second, time.Second, hedgeDelay, "x:1")
	if !errors.Is(o.err, boom) || o.hedged {
		t.Fatalf("%+v", o)
	}
	time.Sleep(2 * hedgeDelay)
	if secondary.Load() != 0 {
		t.Fatal("the secondary started after the primary had failed")
	}
}

func TestRaceDialBothFailReturnsThePrimaryError(t *testing.T) {
	shortRace(t)
	pErr, sErr := errors.New("primary"), errors.New("secondary")
	o := raceDial(context.Background(), pipeDialer(3*hedgeDelay, pErr, nil, nil), pipeDialer(0, sErr, nil, nil), time.Second, time.Second, hedgeDelay, "x:1")
	if !errors.Is(o.err, pErr) || !o.hedged || o.conn != nil {
		t.Fatalf("%+v", o)
	}
}

func TestRaceDialClosesALateWinner(t *testing.T) {
	shortRace(t)
	var closed atomic.Bool
	late := func(ctx context.Context, network, addr string) (net.Conn, error) {
		time.Sleep(3 * hedgeDelay)
		a, b := net.Pipe()
		go func() { b.Read(make([]byte, 1)); closed.Store(true) }()
		return a, nil
	}
	o := raceDial(context.Background(), late, pipeDialer(hedgeDelay, nil, nil, nil), time.Second, time.Second, hedgeDelay, "x:1")
	if o.err != nil || o.winner != 1 {
		t.Fatalf("%+v", o)
	}
	o.conn.Close()
	time.Sleep(8 * hedgeDelay)
	if !closed.Load() {
		t.Fatal("the connection that finished second was not closed")
	}
}

func slowTunnel(now *time.Time, direct, tun dialFunc) (*Tunnel, *group) {
	tt, g := evidenceTunnel(now)
	tt.mode = ModeAuto
	tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1})
	tt.ways[RouteDirect] = way{dial: direct, timeout: time.Second}
	tt.ways[RouteTunnel] = way{dial: tun, timeout: time.Second}
	return tt, g
}

func TestSlowDirectConnectsMoveTheGroupToTheTunnelAndBack(t *testing.T) {
	shortRace(t)
	now := time.Unix(1000, 0)
	var directCalls, tunnelCalls atomic.Int32
	direct := pipeDialer(600*time.Millisecond, nil, &directCalls, nil)
	tun := pipeDialer(5*time.Millisecond, nil, &tunnelCalls, nil)
	tt, g := slowTunnel(&now, direct, tun)
	// Three slow connects: the tunnel wins the race each time.
	for i := 0; i < slowEventsToPrefer; i++ {
		c, route, err := tt.dial(g, "relay.example.org:443")
		if err != nil || route != RouteTunnel {
			t.Fatalf("connect %d: route %v err %v, want the tunnel to win", i, route, err)
		}
		c.Close()
	}
	if !g.slow.active(now) || tt.Status(0).Groups["relay.example.org"].DirectSlowUntil == nil {
		t.Fatal("a slow period did not start")
	}
	if g.route != RouteDirect {
		t.Fatalf("the group route is %v: the slow period must not change the routing state", g.route)
	}
	lines := 0
	for _, e := range tt.log.last(50) {
		if len(e.Msg) > 5 && e.Msg[:5] == "slow:" {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("%d slow lines, want 1", lines)
	}
	// In the period the tunnel goes first and direct is not started.
	before := directCalls.Load()
	c, route, err := tt.dial(g, "relay.example.org:443")
	if err != nil || route != RouteTunnel {
		t.Fatalf("route %v err %v", route, err)
	}
	c.Close()
	time.Sleep(2 * preferDelay)
	if directCalls.Load() != before {
		t.Fatal("direct was started although the tunnel was quick")
	}
	// A slow tunnel lets direct in as the hedge.
	tt.ways[RouteTunnel] = way{dial: pipeDialer(time.Second, nil, nil, nil), timeout: 2 * time.Second}
	tt.ways[RouteDirect] = way{dial: pipeDialer(5*time.Millisecond, nil, nil, nil), timeout: time.Second}
	c, route, err = tt.dial(g, "relay.example.org:443")
	if err != nil || route != RouteDirect {
		t.Fatalf("route %v err %v, want direct as the hedge", route, err)
	}
	c.Close()
	// After the period direct goes first again.
	now = now.Add(4 * time.Minute)
	tt.ways[RouteDirect] = way{dial: pipeDialer(0, nil, nil, nil), timeout: time.Second}
	c, route, err = tt.dial(g, "relay.example.org:443")
	if err != nil || route != RouteDirect {
		t.Fatalf("route %v err %v after the period", route, err)
	}
	c.Close()
}

func TestHedgeDoesNotApplyOutsideTheAutomaticModeOrToOtherTraffic(t *testing.T) {
	shortRace(t)
	now := time.Unix(1000, 0)
	var tunnelCalls atomic.Int32
	tt, g := slowTunnel(&now, pipeDialer(150*time.Millisecond, nil, nil, nil), pipeDialer(0, nil, &tunnelCalls, nil))
	tt.mode = ModeOff
	c, route, err := tt.dial(g, "relay.example.org:443")
	if err != nil || route != RouteDirect || tunnelCalls.Load() != 0 {
		t.Fatalf("route %v err %v tunnel calls %d", route, err, tunnelCalls.Load())
	}
	c.Close()
	tt.mode = ModeAuto
	g.kind = kindOther
	c, route, err = tt.dial(g, "relay.example.org:443")
	if err != nil || route != RouteDirect || tunnelCalls.Load() != 0 {
		t.Fatalf("other traffic: route %v err %v tunnel calls %d", route, err, tunnelCalls.Load())
	}
	c.Close()
}

func TestAFailingDirectStillFallsBackToTheTunnelWithoutHedging(t *testing.T) {
	shortRace(t)
	now := time.Unix(1000, 0)
	var tunnelCalls atomic.Int32
	tt, g := slowTunnel(&now, pipeDialer(0, errors.New("unreachable"), nil, nil), pipeDialer(0, nil, &tunnelCalls, nil))
	c, route, err := tt.dial(g, "relay.example.org:443")
	if err != nil || route != RouteTunnel || tunnelCalls.Load() != 1 {
		t.Fatalf("route %v err %v tunnel calls %d", route, err, tunnelCalls.Load())
	}
	c.Close()
}
