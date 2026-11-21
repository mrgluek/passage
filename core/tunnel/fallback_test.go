package tunnel

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// In a slow period the tunnel goes first; when it fails at once, before the
// direct hedge has started, the healthy direct route must still be tried.
func TestAFastTunnelFailureInASlowPeriodFallsBackToDirect(t *testing.T) {
	shortRace(t)
	now := time.Unix(1000, 0)
	var direct atomic.Int32
	tt, g := slowTunnel(&now, pipeDialer(0, nil, &direct, nil), pipeDialer(0, errors.New("tunnel unavailable"), nil, nil))
	g.slow.until = now.Add(time.Minute)
	c, _, err := tt.dial(g, "relay.example.org:443")
	if c != nil {
		c.Close()
	}
	if err != nil || direct.Load() != 1 {
		t.Fatalf("direct was not used after the tunnel failed: err=%v direct calls=%d", err, direct.Load())
	}
}

// A hedge that was started and failed must not be dialled a second time by the
// fallback loop.
func TestAFailedHedgeIsNotDialledAgain(t *testing.T) {
	shortRace(t)
	now := time.Unix(1000, 0)
	var tunnel atomic.Int32
	tt, g := slowTunnel(&now, pipeDialer(2*hedgeDelay, errors.New("direct unavailable"), nil, nil), pipeDialer(0, errors.New("tunnel unavailable"), &tunnel, nil))
	tt.dial(g, "relay.example.org:443")
	if tunnel.Load() != 1 {
		t.Fatalf("the tunnel was dialled %d times, want 1", tunnel.Load())
	}
}
