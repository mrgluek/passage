package exitdial

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func v4(n byte) netip.Addr { return netip.AddrFrom4([4]byte{192, 0, 2, n}) }
func v6(n byte) netip.Addr { return netip.AddrFrom16([16]byte{0x20, 1, 0x0d, 0xb8, 15: n}) }

func wait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dial goroutine did not finish")
	}
}

func TestAlternate(t *testing.T) {
	for _, tc := range []struct{ in, want []netip.Addr }{
		{nil, nil},
		{[]netip.Addr{v6(1), v6(2), v4(1), v4(2), v6(3)}, []netip.Addr{v6(1), v4(1), v6(2), v4(2), v6(3)}},
		{[]netip.Addr{v4(1), v4(2), v6(1)}, []netip.Addr{v4(1), v6(1), v4(2)}},
	} {
		if got := alternate(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("alternate = %v, want %v", got, tc.want)
		}
	}
}

func TestHangingIPv6FallsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	winner, peer := net.Pipe()
	defer peer.Close()
	var calls atomic.Int32
	start := time.Now()
	conn, err := dial(ctx, []netip.Addr{v6(1), v6(2), v4(1)}, 443, func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || network != "tcp" {
			t.Errorf("unexpected dial: %s %s", network, address)
		}
		if host == v4(1).String() {
			return winner, nil
		}
		defer close(done)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil || conn != winner {
		t.Fatalf("dial = %v, %v", conn, err)
	}
	conn.Close()
	if elapsed := time.Since(start); elapsed >= 900*time.Millisecond {
		t.Fatalf("fallback took %v", elapsed)
	}
	wait(t, done)
	if calls.Load() != 2 {
		t.Fatalf("dialled %d addresses, want 2", calls.Load())
	}
}

func TestSingleFamilyAndImmediateFailure(t *testing.T) {
	for _, addresses := range [][]netip.Addr{{v4(1), v4(2)}, {v6(1), v6(2)}} {
		t.Run(addresses[0].String(), func(t *testing.T) {
			winner, peer := net.Pipe()
			defer peer.Close()
			var calls atomic.Int32
			start := time.Now()
			conn, err := dial(context.Background(), addresses, 443, func(context.Context, string, string) (net.Conn, error) {
				if calls.Add(1) == 1 {
					return nil, errors.New("unreachable")
				}
				return winner, nil
			})
			if err != nil || conn != winner {
				t.Fatalf("dial = %v, %v", conn, err)
			}
			conn.Close()
			if time.Since(start) >= fallbackDelay {
				t.Fatal("failed attempt did not trigger immediate fallback")
			}
		})
	}
}

func TestFirstSuccess(t *testing.T) {
	winner, peer := net.Pipe()
	defer peer.Close()
	var calls atomic.Int32
	conn, err := dial(context.Background(), []netip.Addr{v6(1), v4(1)}, 443, func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return winner, nil
	})
	if err != nil || conn != winner || calls.Load() != 1 {
		t.Fatalf("dial = %v, %v; calls = %d", conn, err, calls.Load())
	}
	conn.Close()
}

func TestAllFail(t *testing.T) {
	baseline := runtime.NumGoroutine()
	want := errors.New("last failure")
	var active atomic.Int32
	var calls atomic.Int32
	for range 100 {
		conn, err := dial(context.Background(), []netip.Addr{v6(1), v6(2), v4(1)}, 443, func(context.Context, string, string) (net.Conn, error) {
			active.Add(1)
			defer active.Add(-1)
			if calls.Add(1)%3 != 0 {
				return nil, errors.New("earlier failure")
			}
			return nil, want
		})
		if conn != nil || !errors.Is(err, want) {
			t.Fatalf("dial = %v, %v", conn, err)
		}
	}
	if active.Load() != 0 || calls.Load() != 300 {
		t.Fatalf("active = %d; calls = %d", active.Load(), calls.Load())
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline+2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > baseline+2 {
		t.Fatalf("goroutines grew from %d to %d", baseline, got)
	}
}

func TestCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			done := make(chan struct{})
			start := time.Now()
			conn, err := dial(ctx, []netip.Addr{v6(1), v4(1)}, 443, func(ctx context.Context, _, _ string) (net.Conn, error) {
				defer close(done)
				if !deadline {
					cancel()
				}
				<-ctx.Done()
				return nil, ctx.Err()
			})
			if conn != nil || !errors.Is(err, want) {
				t.Fatalf("dial = %v, %v", conn, err)
			}
			if time.Since(start) >= time.Second {
				t.Fatal("cancellation was slow")
			}
			wait(t, done)
		})
	}
}

func TestLateSuccessIsClosed(t *testing.T) {
	late, peer := net.Pipe()
	defer peer.Close()
	winner, winnerPeer := net.Pipe()
	defer winnerPeer.Close()
	done := make(chan struct{})
	var calls atomic.Int32
	conn, err := dial(context.Background(), []netip.Addr{v6(1), v4(1)}, 443, func(ctx context.Context, _, _ string) (net.Conn, error) {
		if calls.Add(1) == 1 {
			defer close(done)
			<-ctx.Done()
			return late, nil
		}
		return winner, nil
	})
	if err != nil || conn != winner {
		t.Fatalf("dial = %v, %v", conn, err)
	}
	conn.Close()
	wait(t, done)
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("late connection was not closed: %v", err)
	}
}

func TestAttemptTimeout(t *testing.T) {
	done := make(chan struct{})
	start := time.Now()
	_, err := dial(context.Background(), []netip.Addr{v4(1)}, 443, func(ctx context.Context, _, _ string) (net.Conn, error) {
		defer close(done)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 4*time.Second {
		t.Fatalf("attempt timeout: %v after %v", err, time.Since(start))
	}
	wait(t, done)
}

func TestEmptyAndCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, addresses := range [][]netip.Addr{nil, {v4(1)}} {
		conn, err := dial(ctx, addresses, 443, func(context.Context, string, string) (net.Conn, error) {
			t.Error("unexpected dial")
			return nil, nil
		})
		if conn != nil || err == nil {
			t.Fatalf("dial = %v, %v", conn, err)
		}
	}
}
