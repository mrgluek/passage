package tunnel

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

type idleHelper struct {
	*irohClient
	pool          *routePool
	starts, stops atomic.Int32
	dialHook      func() error
	stopHook      func()
}

func newIdleHelper(t *testing.T) *idleHelper {
	t.Helper()
	client, err := newRoutePool(Config{PrivateDir: t.TempDir(), Routes: []links.Route{{ID: "route-a", Type: "iroh"}}})
	if err != nil {
		t.Fatal(err)
	}
	p := client.(*routePool)
	p.log = newEventLog(10)
	h := &idleHelper{irohClient: p.clients[0].(*irohClient), pool: p}
	h.start = func(life context.Context) (dialFunc, func(), error) {
		h.starts.Add(1)
		return func(context.Context, string, string) (net.Conn, error) {
				if h.dialHook != nil {
					if err := h.dialHook(); err != nil {
						return nil, err
					}
				}
				a, b := net.Pipe()
				b.Close()
				return a, nil
			}, func() {
				if life.Err() == nil {
					t.Error("helper lifetime still active during cleanup")
				}
				h.stops.Add(1)
				if h.stopHook != nil {
					h.stopHook()
				}
			}, nil
	}
	return h
}

func idleDial(t *testing.T, dial dialFunc) net.Conn {
	t.Helper()
	c, err := dial(context.Background(), "tcp", "target:1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func idleAdvance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func TestIrohIdleDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dial    func(*idleHelper) dialFunc
		extends bool
	}{
		{"traffic", func(h *idleHelper) dialFunc { return h.pool.DialContext }, true},
		{"maintenance", func(h *idleHelper) dialFunc { return h.pool.maintenanceDialContext }, true},
		{"confirmation", func(h *idleHelper) dialFunc { return h.pool.probeDialContext }, false},
		{"network test", func(h *idleHelper) dialFunc { return h.pool.ways()[0].dial }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newIdleHelper(t)
				defer h.Close()
				idleDial(t, h.pool.DialContext).Close()
				idleAdvance(irohIdleTimeout - time.Nanosecond)
				stream := idleDial(t, tc.dial(h))
				idleAdvance(2 * irohIdleTimeout)
				if h.stops.Load() != 0 {
					t.Fatal("stopped with an open stream")
				}
				stream.Close()
				stream.Close()
				if tc.extends {
					idleAdvance(irohIdleTimeout - time.Nanosecond)
					if h.stops.Load() != 0 {
						t.Fatal("stopped early")
					}
					idleAdvance(time.Nanosecond)
				}
				synctest.Wait()
				if h.stops.Load() != 1 || h.dial != nil || h.timer != nil || h.active != 0 {
					t.Fatal("idle helper resources remain")
				}
				idleDial(t, h.pool.DialContext).Close()
				if h.starts.Load() != 2 || h.stops.Load() != 1 {
					t.Fatalf("starts=%d stops=%d", h.starts.Load(), h.stops.Load())
				}
			})
		})
	}
}

func TestIrohIdlePreservesEvidence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newIdleHelper(t)
		defer h.Close()
		idleDial(t, h.pool.DialContext).Close()
		h.failures = 2
		h.lastRestart = time.Now()
		lastRestart := h.lastRestart
		before := h.pool.states()
		h.pool.onFailure = func() { t.Error("idle triggered pool failure") }
		idleAdvance(irohIdleTimeout)
		events := h.pool.log.last(10)
		if len(events) != 1 || events[0].Msg != "iroh helper stopped: idle" || !events[0].Time.Equal(time.Now()) {
			t.Fatalf("history = %v", events)
		}
		if !reflect.DeepEqual(before, h.pool.states()) || !h.pool.Established() || len(h.pool.failedUntil) != 0 ||
			h.lastRestart != lastRestart || h.failures != 2 {
			t.Fatal("idle stop changed route or restart evidence")
		}
	})
}

func TestIrohSleepingProbe(t *testing.T) {
	for _, tc := range []struct {
		name       string
		otherError error
		wake       bool
	}{
		{"alternative available", nil, false},
		{"alternative failed", errors.New("unavailable"), true},
		{"alternative refused", errDestinationRefused, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newIdleHelper(t)
				defer h.Close()
				idleDial(t, h.pool.DialContext).Close()
				idleAdvance(irohIdleTimeout)
				h.pool.clients = append(h.pool.clients, &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
					if tc.otherError != nil {
						return nil, tc.otherError
					}
					a, b := net.Pipe()
					b.Close()
					return a, nil
				}})
				idleDial(t, h.pool.probeDialContext).Close()
				synctest.Wait()
				if (h.starts.Load() == 2) != tc.wake || h.starts.Load() != h.stops.Load() || !h.pool.failedUntil[0].IsZero() {
					t.Fatalf("starts=%d stops=%d holds=%v", h.starts.Load(), h.stops.Load(), h.pool.failedUntil)
				}
			})
		})
	}
}

func TestIrohIdleInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newIdleHelper(t)
		defer h.Close()
		idleDial(t, h.DialContext).Close()
		idleAdvance(irohIdleTimeout - time.Nanosecond)
		entered, resume := make(chan struct{}), make(chan struct{})
		h.dialHook = func() error {
			close(entered)
			<-resume
			return nil
		}
		result := make(chan net.Conn)
		go func() {
			c, _ := h.DialContext(context.Background(), "tcp", "target:1")
			result <- c
		}()
		<-entered
		idleAdvance(irohIdleTimeout)
		if h.stops.Load() != 0 {
			t.Fatal("stopped a pending dial")
		}
		close(resume)
		c := <-result
		if c == nil {
			t.Fatal("pending dial failed")
		}
		c.Close()
		idleAdvance(irohIdleTimeout)
		if h.starts.Load() != 1 || h.stops.Load() != 1 {
			t.Fatal("helper did not stop after dial")
		}
	})
}

func TestIrohDialAndCloseDuringIdleStop(t *testing.T) {
	for _, closing := range []bool{false, true} {
		name := "dial"
		if closing {
			name = "close"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newIdleHelper(t)
				defer h.Close()
				entered, resume := make(chan struct{}), make(chan struct{})
				h.stopHook = func() {
					close(entered)
					<-resume
					h.stopHook = nil
				}
				idleDial(t, h.DialContext).Close()
				<-entered // virtual time advances to the timer
				attempting := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					close(attempting)
					if closing {
						done <- h.Close()
						return
					}
					c, err := h.DialContext(context.Background(), "tcp", "target:1")
					if c != nil {
						c.Close()
					}
					done <- err
				}()
				<-attempting
				close(resume)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if closing {
					if _, err := h.DialContext(context.Background(), "tcp", "target:1"); !errors.Is(err, net.ErrClosed) {
						t.Fatal(err)
					}
					if h.starts.Load() != 1 || h.stops.Load() != 1 {
						t.Fatal("closed helper relaunched")
					}
				} else if h.starts.Load() != 2 || h.stops.Load() != 1 {
					t.Fatal("dial did not start fresh helper")
				}
				h.Close()
				idleAdvance(2 * irohIdleTimeout)
				if h.starts.Load() != h.stops.Load() || len(h.pool.log.last(10)) != 1 {
					t.Fatal("close left idle work pending")
				}
			})
		})
	}
}

func TestIrohOldIdleTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newIdleHelper(t)
		defer h.Close()
		var callback func()
		h.afterFunc = func(d time.Duration, f func()) *time.Timer {
			callback = f
			return time.AfterFunc(d, f)
		}
		first := idleDial(t, h.DialContext)
		second := idleDial(t, h.DialContext)
		first.Close()
		idleAdvance(irohIdleTimeout)
		if h.stops.Load() != 0 {
			t.Fatal("closing one stream stopped another")
		}
		second.Close()
		old := callback
		idleAdvance(irohIdleTimeout)
		idleDial(t, h.DialContext).Close()
		old()
		if h.stops.Load() != 1 || h.timer == nil {
			t.Fatal("old callback disturbed new helper")
		}
		h.Close()
		idleAdvance(irohIdleTimeout)
		if h.stops.Load() != 2 || len(h.pool.log.last(10)) != 1 {
			t.Fatal("terminal close left a timer or logged an idle stop")
		}
	})
}

func TestIrohUnsuccessfulDialsDoNotExtendIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newIdleHelper(t)
		defer h.Close()
		h.dialHook = func() error { return errDestinationRefused }
		for range 10 {
			if _, err := h.DialContext(context.Background(), "tcp", "target:1"); !errors.Is(err, errDestinationRefused) {
				t.Fatal(err)
			}
			idleAdvance(irohIdleTimeout / 10)
		}
		if h.starts.Load() != 1 || h.stops.Load() != 1 {
			t.Fatal("failed dials retained helper")
		}
	})
}

func (h *idleHelper) running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dial != nil
}

func TestIrohFailedDialAfterIdleStopKeepsTheNewHelper(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newIdleHelper(t)
		defer h.Close()
		idleDial(t, h.pool.DialContext).Close()
		idleAdvance(irohIdleTimeout)
		if h.stops.Load() != 1 || h.running() {
			t.Fatal("helper did not stop when idle")
		}
		h.dialHook = func() error { return errDestinationRefused }
		if _, err := h.DialContext(context.Background(), "tcp", "target:1"); !errors.Is(err, errDestinationRefused) {
			t.Fatal(err)
		}
		idleAdvance(time.Second)
		if h.starts.Load() != 2 || h.stops.Load() != 1 || !h.running() {
			t.Fatalf("a failed dial after an idle stop stopped the new helper: starts=%d stops=%d", h.starts.Load(), h.stops.Load())
		}
		idleAdvance(irohIdleTimeout)
		if h.stops.Load() != 2 || h.running() {
			t.Fatal("the new helper never went back to sleep")
		}
	})
}
