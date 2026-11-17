package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
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

// A source that exits refuse is fetched directly in the first tier with the
// short timeout; when that is too short, the direct tier must still try it.
func TestARefusedSourceStillGetsTheLongDirectDeadline(t *testing.T) {
	subscriptionTestHTTP = true
	defer func() { subscriptionTestHTTP = false }()
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint("refused=", refused), func(t *testing.T) {
			sub, key, bundle := subFixture(t)
			raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
			if err != nil {
				t.Fatal(err)
			}
			sub.Sources = []string{"http://example.org/source"}
			if refused {
				sub.SourceStats = map[string]SubscriptionSourceStats{sourceStatsKey(sub.Sources[0]): {TunnelRefusedAt: time.Now().Unix()}}
			}
			ref := &SubscriptionRefresher{tunnelTimeout: 40 * time.Millisecond, directTimeout: 400 * time.Millisecond, directDial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				a, b := net.Pipe()
				go func() {
					defer b.Close()
					if _, err := http.ReadRequest(bufio.NewReader(b)); err != nil {
						return
					}
					time.Sleep(100 * time.Millisecond)
					fmt.Fprintf(b, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(raw), raw)
				}()
				return a, nil
			}}
			out := ref.Refresh(context.Background(), Config{Subscriptions: []Subscription{sub}}, []SubscriptionDial{func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused }})
			if got := out.Config.Subscriptions[0]; got.LastOK == 0 {
				t.Fatalf("the direct fetch did not finish: LastError=%s", got.LastError)
			}
		})
	}
}
