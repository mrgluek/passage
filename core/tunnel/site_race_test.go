package tunnel

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSiteTimer records the head start asked for and fires when the test says.
type fakeSiteTimer struct {
	asked chan time.Duration
	fire  chan time.Time
}

func useFakeSiteTimer(tt *Tunnel) *fakeSiteTimer {
	f := &fakeSiteTimer{asked: make(chan time.Duration, 4), fire: make(chan time.Time)}
	tt.siteAfter = func(d time.Duration) (<-chan time.Time, func()) {
		f.asked <- d
		return f.fire, func() {}
	}
	return f
}

// blockDialer waits until its context ends, as a dropped SYN does.
func blockDialer(calls *atomic.Int32) dialFunc {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

func (f *fakeSiteTimer) wait(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-f.asked:
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("no head start was started")
		return 0
	}
}

type siteDial struct {
	conn  net.Conn
	route Route
	err   error
}

func dialSiteAsync(tt *Tunnel, host string) chan siteDial {
	out := make(chan siteDial, 1)
	go func() {
		c, r, _, err := tt.dialSite(tt.sites.match(host), host+":443")
		out <- siteDial{c, r, err}
	}()
	return out
}

func TestTunnelSiteStartsTheTunnelAfterTheHeadStart(t *testing.T) {
	var d, tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, blockDialer(&d), echoDialer("t:", &tu))
	tt.ways[RouteDirect] = way{blockDialer(&d), 300 * time.Millisecond}
	clock := useFakeSiteTimer(tt)
	out := dialSiteAsync(tt, "example.net")
	if got := clock.wait(t); got != 2*time.Second {
		t.Fatalf("head start %v", got)
	}
	select {
	case <-out:
		t.Fatal("finished before the tunnel started")
	case <-time.After(50 * time.Millisecond):
	}
	if tu.Load() != 0 {
		t.Fatal("tunnel dialed before the head start ended")
	}
	clock.fire <- time.Now()
	res := <-out
	if res.err != nil || res.route != RouteTunnel || tu.Load() != 1 {
		t.Fatalf("%+v, tunnel calls %d", res, tu.Load())
	}
	res.conn.Close()
	// The direct dial runs out in the background and marks the route bad.
	e := tt.sites.match("example.net")
	deadline := time.Now().Add(2 * time.Second)
	for !e.badUntil(RouteDirect).After(time.Now()) {
		if time.Now().After(deadline) {
			t.Fatal("the blocked direct route was not remembered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Next connection: the tunnel starts at once, no head start.
	res = <-dialSiteAsync(tt, "example.net")
	if res.err != nil || res.route != RouteTunnel || len(clock.asked) != 0 {
		t.Fatalf("%+v, timers %d", res, len(clock.asked))
	}
	res.conn.Close()
}

func TestTunnelSiteFastDirectDialsNoTunnel(t *testing.T) {
	var d, tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &d), echoDialer("t:", &tu))
	useFakeSiteTimer(tt)
	res := <-dialSiteAsync(tt, "example.net")
	if res.err != nil || res.route != RouteDirect || tu.Load() != 0 {
		t.Fatalf("%+v, tunnel calls %d", res, tu.Load())
	}
	res.conn.Close()
}

func TestTunnelSiteStartsAtOnceWhenDirectFails(t *testing.T) {
	var d, tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, failDialer(&d), echoDialer("t:", &tu))
	clock := useFakeSiteTimer(tt)
	out := dialSiteAsync(tt, "example.net")
	clock.wait(t) // never fired
	res := <-out
	if res.err != nil || res.route != RouteTunnel {
		t.Fatalf("%+v", res)
	}
	res.conn.Close()
}

// A route that connects after another one won is closed and not marked bad.
func TestTunnelSiteLateLoserIsClosed(t *testing.T) {
	release := make(chan struct{})
	closed := make(chan struct{})
	late := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-release
		a, b := net.Pipe()
		go func() {
			b.Read(make([]byte, 1)) // returns when a is closed
			close(closed)
		}()
		return a, nil
	}
	var tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, late, echoDialer("t:", &tu))
	tt.ways[RouteDirect] = way{late, 5 * time.Second}
	clock := useFakeSiteTimer(tt)
	out := dialSiteAsync(tt, "example.net")
	clock.wait(t)
	clock.fire <- time.Now()
	res := <-out
	if res.err != nil || res.route != RouteTunnel {
		t.Fatalf("%+v", res)
	}
	defer res.conn.Close()
	close(release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the late direct connection leaked")
	}
	if tt.sites.match("example.net").badUntil(RouteDirect).After(time.Now()) {
		t.Fatal("a route that connected was marked bad")
	}
}

// Smart sites keep the plain order: no timer, no tunnel.
func TestSmartSiteNeedsNoHeadStart(t *testing.T) {
	var d, tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &d), echoDialer("t:", &tu))
	clock := useFakeSiteTimer(tt)
	res := <-dialSiteAsync(tt, "example.org")
	if res.err != nil || res.route != RouteDirect || len(clock.asked) != 0 {
		t.Fatalf("%+v", res)
	}
	res.conn.Close()
}
