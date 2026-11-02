package tunnel

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type closeFlag struct {
	net.Conn
	closed atomic.Bool
}

func (c *closeFlag) Close() error { c.closed.Store(true); return c.Conn.Close() }

func staggerFor(t *testing.T, d time.Duration) {
	t.Helper()
	old := routeStagger
	routeStagger = d
	t.Cleanup(func() { routeStagger = old })
}

func TestRoutePoolStartsNextAfterStagger(t *testing.T) {
	staggerFor(t, 50*time.Millisecond)
	release := make(chan struct{})
	late := &closeFlag{}
	slow := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		<-release // ignores cancellation: wins only after the race is over
		a, b := net.Pipe()
		b.Close()
		late.Conn = a
		return late, nil
	}}
	a, b := net.Pipe()
	defer b.Close()
	fast := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return a, nil }}
	pool := &routePool{clients: []soarClient{slow, fast}, ipv6: []bool{false, false}, kinds: []string{"iroh", "wss"}}
	start := time.Now()
	conn, err := pool.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil || connVia(conn) != "wss" {
		t.Fatalf("want the second route, got %v %v", connVia(conn), err)
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > time.Second {
		t.Fatalf("second route started after %v", d)
	}
	close(release)
	waitFor(t, late.closed.Load)
	if len(pool.failedUntil) != 0 {
		t.Fatal("the slower route was penalized for losing")
	}
}

func TestRoutePoolFastRouteStartsNoOther(t *testing.T) {
	staggerFor(t, 50*time.Millisecond)
	a, b := net.Pipe()
	defer b.Close()
	var second atomic.Int32
	first := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return a, nil }}
	other := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		second.Add(1)
		return nil, errors.New("unused")
	}}
	pool := &routePool{clients: []soarClient{first, other}, ipv6: []bool{false, false}}
	if _, err := pool.DialContext(context.Background(), "tcp", "example.org:443"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if second.Load() != 0 {
		t.Fatal("a second route started although the first connected")
	}
}

func TestRoutePoolFailureStartsNextAtOnce(t *testing.T) {
	staggerFor(t, time.Second)
	a, b := net.Pipe()
	defer b.Close()
	down := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("down") }}
	up := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return a, nil }}
	pool := &routePool{clients: []soarClient{down, up}, ipv6: []bool{false, false}}
	start := time.Now()
	if _, err := pool.DialContext(context.Background(), "tcp", "example.org:443"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("waited %v after a fast failure", d)
	}
	if len(pool.failedUntil) != 1 {
		t.Fatal("the failed route was not put behind the others")
	}
}

func TestRoutePoolCallerCancelClosesLateWinners(t *testing.T) {
	staggerFor(t, time.Hour)
	release := make(chan struct{})
	late := &closeFlag{}
	slow := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		<-release
		a, b := net.Pipe()
		b.Close()
		late.Conn = a
		return late, nil
	}}
	pool := &routePool{clients: []soarClient{slow}, ipv6: []bool{false}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := pool.DialContext(ctx, "tcp", "example.org:443"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(release)
	waitFor(t, late.closed.Load)
}
