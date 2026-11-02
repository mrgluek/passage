package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/getlantern/soar"
)

type poolFixture struct {
	dial   dialFunc
	closed bool
}

func (f *poolFixture) DialContext(c context.Context, n, a string) (net.Conn, error) {
	return f.dial(c, n, a)
}
func (f *poolFixture) Established() bool          { return true }
func (f *poolFixture) PathStats() []soar.PathStat { return nil }
func (f *poolFixture) Close() error               { f.closed = true; return nil }
func TestRoutePoolFallsBackAndClosesAll(t *testing.T) {
	attempts := 0
	first := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		attempts++
		return nil, errors.New("unavailable")
	}}
	a, b := net.Pipe()
	defer b.Close()
	second := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { attempts++; return a, nil }}
	pool := &routePool{clients: []soarClient{first, second}, ipv6: []bool{false, false}}
	conn, err := pool.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil || attempts != 2 || !pool.Established() {
		t.Fatal("fallback failed", err)
	}
	conn.Close()
	pool.Close()
	if !first.closed || !second.closed {
		t.Fatal("client cleanup missing")
	}
}

func TestRoutePoolSkipsFailedRouteUntilCooldown(t *testing.T) {
	up := false
	var tried []string
	primary := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		tried = append(tried, "primary")
		if !up {
			return nil, errors.New("unavailable")
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}}
	backup := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		tried = append(tried, "backup")
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}}
	now := time.Unix(1000, 0)
	pool := &routePool{clients: []soarClient{primary, backup}, ipv6: []bool{false, false}, now: func() time.Time { return now }}
	dial := func() {
		t.Helper()
		conn, err := pool.DialContext(context.Background(), "tcp", "example.org:443")
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	check := func(want ...string) {
		t.Helper()
		if fmt.Sprint(tried) != fmt.Sprint(want) {
			t.Fatalf("tried %v, want %v", tried, want)
		}
		tried = nil
	}
	dial()
	check("primary", "backup")
	dial()
	check("backup")
	up = true
	now = now.Add(routeCooldown)
	dial()
	check("primary")
	dial()
	check("primary")
}

func TestRoutePoolRetriesCoolingRoutesWhenNothingElse(t *testing.T) {
	attempts := 0
	only := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		attempts++
		return nil, errors.New("unavailable")
	}}
	pool := &routePool{clients: []soarClient{only}, ipv6: []bool{false}}
	for range 2 {
		if _, err := pool.DialContext(context.Background(), "tcp", "example.org:443"); err == nil {
			t.Fatal("dial succeeded")
		}
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRoutePoolReportsViaAndStates(t *testing.T) {
	down := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unavailable") }}
	up := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}}
	pool := &routePool{clients: []soarClient{down, up}, ipv6: []bool{false, false},
		kinds: []string{"iroh", "soar"}, ids: []string{"fast", "dns"}}
	conn, err := pool.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if via := connVia(conn); via != "soar" {
		t.Fatalf("via = %q, want soar", via)
	}
	if _, ok := conn.(interface{ CloseWrite() error }); !ok {
		t.Fatal("wrapped connection lost CloseWrite")
	}
	want := []RouteState{{"fast", "iroh", routeFailed}, {"dns", "soar", routeUp}}
	if got := pool.states(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
}

func TestRoutePoolWaysUseIDsForSharedTypes(t *testing.T) {
	f := &poolFixture{}
	pool := &routePool{clients: []soarClient{f, f, f}, ipv6: []bool{false, true, false},
		kinds: []string{"iroh", "soar", "soar"}, ids: []string{"fast", "dns-a", "dns-b"}}
	var names []string
	for _, w := range pool.ways() {
		names = append(names, w.name)
	}
	if fmt.Sprint(names) != "[iroh dns-a dns-b]" {
		t.Fatalf("names = %v", names)
	}
}

func TestDestinationFailureDoesNotPenalizeRoute(t *testing.T) {
	primary := &poolFixture{dial: func(_ context.Context, _, address string) (net.Conn, error) {
		if address == "down.example.org:443" {
			return nil, errDestinationRefused
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}}
	backup := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("backup unavailable")
	}}
	pool := &routePool{clients: []soarClient{primary, backup}, ipv6: []bool{false, false}}
	if _, err := pool.DialContext(context.Background(), "tcp", "down.example.org:443"); err == nil {
		t.Fatal("unavailable destination connected")
	}
	if _, penalized := pool.failedUntil[0]; penalized {
		t.Fatal("destination failure penalized primary")
	}
	conn, err := pool.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}
