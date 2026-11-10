package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

func poolSuccess(context.Context, string, string) (net.Conn, error) {
	a, b := net.Pipe()
	b.Close()
	return a, nil
}

func TestReserveWaitsForAllPrimaryFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	var attempts []string
	primary := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		attempts = append(attempts, "primary")
		return nil, errors.New("unavailable")
	}}
	reserve := &poolFixture{dial: func(c context.Context, n, a string) (net.Conn, error) {
		attempts = append(attempts, "reserve")
		return poolSuccess(c, n, a)
	}}
	p := &routePool{clients: []soarClient{reserve, primary}, ipv6: []bool{false, false}, uses: []string{"reserve", ""}, now: func() time.Time { return now }}
	for _, want := range [][]string{{"primary", "reserve"}, {"reserve"}, {"primary", "reserve"}} {
		conn, err := p.DialContext(context.Background(), "tcp", "example.org:443")
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if !slices.Equal(attempts, want) {
			t.Fatalf("attempts %v, want %v", attempts, want)
		}
		attempts = nil
		now = now.Add(routeCooldown / 2)
	}
}

func TestReserveNeverHedged(t *testing.T) {
	old := routeStagger
	routeStagger = time.Millisecond
	t.Cleanup(func() { routeStagger = old })
	var reserveCalls atomic.Int32
	primaryStarted := make(chan struct{})
	primary := &poolFixture{dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(primaryStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	reserve := &poolFixture{dial: func(c context.Context, n, a string) (net.Conn, error) {
		reserveCalls.Add(1)
		return poolSuccess(c, n, a)
	}}
	p := &routePool{clients: []soarClient{primary, reserve}, ipv6: []bool{false, false}, uses: []string{"", "reserve"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := p.DialContext(ctx, "tcp", "example.org:443")
	<-primaryStarted
	if err == nil || reserveCalls.Load() != 0 {
		t.Fatal("reserve raced a pending primary")
	}
	// A reserve in progress must also run without another staggered start.
	first := &poolFixture{dial: func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }}
	p = &routePool{clients: []soarClient{first, reserve}, ipv6: []bool{false, false}, uses: []string{"reserve", "reserve"}}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = p.DialContext(ctx, "tcp", "example.org:443")
	if err == nil || reserveCalls.Load() != 0 {
		t.Fatal("reserve routes were hedged")
	}
}

func TestReserveUnavailablePrimaryAndBackground(t *testing.T) {
	now := time.Unix(1000, 0)
	var reserveCalls atomic.Int32
	primary := &poolFixture{dial: poolSuccess}
	reserve := &poolFixture{dial: func(c context.Context, n, a string) (net.Conn, error) {
		reserveCalls.Add(1)
		return poolSuccess(c, n, a)
	}}
	p := &routePool{clients: []soarClient{reserve, primary}, ipv6: []bool{false, false}, uses: []string{"reserve", ""}, now: func() time.Time { return now }, failedUntil: map[int]time.Time{1: now.Add(routeCooldown)}}
	conn, err := p.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil || reserveCalls.Load() != 1 {
		t.Fatal("reserve not used when all primaries down", err)
	}
	conn.Close()
	conn, err = p.probeDialContext(context.Background(), "tcp", "example.org:443")
	if err != nil || reserveCalls.Load() != 1 {
		t.Fatal("background probe used reserve", err)
	}
	conn.Close()
	p.clients = p.clients[:1]
	p.ipv6 = p.ipv6[:1]
	if _, err = p.probeDialContext(context.Background(), "tcp", "example.org:443"); err == nil || reserveCalls.Load() != 1 {
		t.Fatal("reserve-only pool warmed")
	}
}

func TestReservePoolMetadataStatusAndHistory(t *testing.T) {
	r := subRoute("one")
	r.Use, r.Exit = "reserve", "shared"
	tun, err := Start(Config{Routes: []links.Route{r}, Mode: ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	p := tun.client.(*routePool)
	status := tun.Status(0)
	raw, err := json.Marshal(status)
	if err != nil || len(status.Tunnels) != 1 || status.Tunnels[0].Exit != "shared" || status.Tunnels[0].Use != "reserve" || !strings.Contains(string(raw), `"exit":"shared"`) {
		t.Fatal("route metadata missing from status", err)
	}
	if p.ways()[0].exit != "shared" {
		t.Fatal("diagnostics lost exit")
	}
	now := time.Unix(1000, 0)
	p = &routePool{log: tun.log, ids: []string{"one"}, exits: []string{"shared"}, now: func() time.Time { return now }, network: p.network}
	p.stalls = map[string]map[int]*hold{p.network: {0: {state: Held, until: now.Add(time.Minute), step: 1}}}
	feedback := &poolConn{pool: p, index: 0, network: p.network}
	feedback.feedback(false)
	events := tun.log.last(10)
	found := false
	for _, e := range events {
		found = found || strings.Contains(e.Msg, "exit=shared")
	}
	if !found {
		t.Fatal("history lost exit label")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = tun.probeTunnel(ctx, "example.org:443", "example.org"); err == nil {
		t.Fatal("reserve warmup succeeded")
	}
}

func TestReserveDoesNotFollowDestinationRefusal(t *testing.T) {
	now := time.Unix(1000, 0)
	var calls atomic.Int32
	primary := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused }}
	reserve := &poolFixture{dial: func(c context.Context, n, a string) (net.Conn, error) { calls.Add(1); return poolSuccess(c, n, a) }}
	p := &routePool{clients: []soarClient{primary, reserve}, ipv6: []bool{false, false}, uses: []string{"", "reserve"}, now: func() time.Time { return now }}
	for range 2 {
		if _, err := p.DialContext(context.Background(), "tcp", "example.org:443"); err == nil {
			t.Fatal("destination refusal activated reserve")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("reserve started while primary was available")
	}
}

func TestReserveRefusalStaysRefusal(t *testing.T) {
	for _, established := range []bool{false, true} {
		name := "idle"
		if established {
			name = "established"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			var primaryCalls, reserveCalls atomic.Int32
			failures := 0
			primary := &poolFixture{dial: func(c context.Context, n, a string) (net.Conn, error) {
				primaryCalls.Add(1)
				if a == "refused.example.org:443" {
					return nil, errDestinationRefused
				}
				return poolSuccess(c, n, a)
			}}
			reserve := &poolFixture{dial: func(c context.Context, n, a string) (net.Conn, error) {
				reserveCalls.Add(1)
				return poolSuccess(c, n, a)
			}}
			p := &routePool{
				clients: []soarClient{primary, reserve}, ipv6: []bool{false, false},
				uses: []string{"", "reserve"}, now: func() time.Time { return now },
				onFailure: func() { failures++ },
			}
			p.established.Store(established)
			refused := func(wantCalls int32) {
				t.Helper()
				_, err := p.DialContext(context.Background(), "tcp", "refused.example.org:443")
				if !errors.Is(err, errDestinationRefused) {
					t.Errorf("dial error = %v, want destination refusal", err)
				}
				if p.Established() != established || failures != 0 {
					t.Errorf("established = %v, want %v; failure callbacks = %d", p.Established(), established, failures)
				}
				if got := primaryCalls.Load(); got != wantCalls {
					t.Errorf("primary calls = %d, want %d", got, wantCalls)
				}
				if reserveCalls.Load() != 0 || len(p.failedUntil) != 0 {
					t.Error("refusal activated reserve or penalized primary")
				}
			}
			refused(1)
			now = now.Add(refusedFor / 2)
			refused(1)
			conn, err := p.DialContext(context.Background(), "tcp", "example.org:443")
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			if primaryCalls.Load() != 2 || reserveCalls.Load() != 0 {
				t.Fatal("different destination did not use primary")
			}
			established = true
			now = now.Add(refusedFor / 2)
			refused(3)
		})
	}
}

func TestReserveStartsForUnavailablePrimary(t *testing.T) {
	for _, state := range []string{"failed", "cooling"} {
		t.Run(state, func(t *testing.T) {
			now := time.Unix(1000, 0)
			var primaryCalls, reserveCalls atomic.Int32
			primary := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
				primaryCalls.Add(1)
				return nil, errors.New("unavailable")
			}}
			reserve := &poolFixture{dial: func(c context.Context, n, a string) (net.Conn, error) {
				reserveCalls.Add(1)
				return poolSuccess(c, n, a)
			}}
			p := &routePool{clients: []soarClient{primary, reserve}, ipv6: []bool{false, false},
				uses: []string{"", "reserve"}, now: func() time.Time { return now }}
			wantPrimaryCalls := int32(1)
			if state == "cooling" {
				p.mark(0, true)
				wantPrimaryCalls = 0
			}
			conn, err := p.DialContext(context.Background(), "tcp", "example.org:443")
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			if primaryCalls.Load() != wantPrimaryCalls || reserveCalls.Load() != 1 || !p.Established() {
				t.Fatalf("primary calls = %d, reserve calls = %d, established = %v", primaryCalls.Load(), reserveCalls.Load(), p.Established())
			}
		})
	}
}

func TestReservePriorityAndManualCheck(t *testing.T) {
	var attempts []string
	fail := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		attempts = append(attempts, "first")
		return nil, errors.New("unavailable")
	}}
	pass := &poolFixture{dial: func(c context.Context, n, a string) (net.Conn, error) {
		attempts = append(attempts, "second")
		return poolSuccess(c, n, a)
	}}
	p := &routePool{clients: []soarClient{fail, pass}, ipv6: []bool{false, false}, uses: []string{"reserve", "reserve"}, kinds: []string{"wss", "wss"}, ids: []string{"one", "two"}}
	conn, err := p.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if !slices.Equal(attempts, []string{"first", "second"}) {
		t.Fatal("reserve priority changed")
	}
	attempts = nil
	conn, err = p.ways()[1].dial(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if !slices.Equal(attempts, []string{"second"}) {
		t.Fatal("manual check excluded reserve")
	}
}
