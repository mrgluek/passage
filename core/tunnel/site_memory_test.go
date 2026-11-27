package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func countingExit(calls *atomic.Int32, fail bool) *poolFixture {
	return &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		if fail {
			return nil, errors.New("down")
		}
		a, b := net.Pipe()
		go func() {
			buf := make([]byte, 64)
			for {
				if _, err := b.Read(buf); err != nil {
					return
				}
			}
		}()
		return a, nil
	}}
}

func TestPreferredPoolRouteIsTriedFirstAndChangesNothing(t *testing.T) {
	staggerFor(t, time.Second)
	var a, b atomic.Int32
	pool := &routePool{clients: []soarClient{countingExit(&a, false), countingExit(&b, false)}, ipv6: []bool{false, false}, ids: []string{"one", "two"}}
	ctx := context.WithValue(context.Background(), preferRouteKey{}, "two")
	c, err := pool.siteDialContext(ctx, "tcp", "example.net:443")
	if err != nil || routeIDOf(c) != "two" || a.Load() != 0 || b.Load() != 1 {
		t.Fatalf("%v %q, calls %d %d", err, routeIDOf(c), a.Load(), b.Load())
	}
	c.Close()
	if len(pool.failedUntil) != 0 {
		t.Fatal("pool state changed")
	}
}

// siteMemoryTunnel has a refusing direct route (not marked bad) and a pool of
// two exits where the first is down.
func siteMemoryTunnel(t *testing.T) (*Tunnel, *atomic.Int32, *atomic.Int32, *atomic.Int32) {
	staggerFor(t, time.Second)
	var d, one, two atomic.Int32
	refuse := func(context.Context, string, string) (net.Conn, error) {
		d.Add(1)
		return nil, fmt.Errorf("x: %w", errDestinationRefused)
	}
	pool := &routePool{clients: []soarClient{countingExit(&one, true), countingExit(&two, false)}, ipv6: []bool{false, false}, ids: []string{"one", "two"}}
	tt := newBrowserTunnel(t, testSites, refuse, pool.DialContext)
	tt.ways[RouteDirect] = way{refuse, time.Second}
	tt.ways[RouteTunnel] = way{pool.DialContext, 5 * time.Second}
	tt.client = pool
	useFakeSiteTimer(tt)
	return tt, &d, &one, &two
}

func TestRememberedExitIsTriedFirstAndAlone(t *testing.T) {
	tt, d, one, two := siteMemoryTunnel(t)
	res := <-dialSiteAsync(tt, "example.net")
	if res.err != nil || res.route != RouteTunnel || routeIDOf(res.conn) != "two" {
		t.Fatalf("%+v", res)
	}
	res.conn.Close()
	w, ok := tt.sites.match("example.net").winner(time.Now())
	if !ok || w.route != RouteTunnel || w.exit != "two" {
		t.Fatalf("not remembered: %+v %v", w, ok)
	}
	d0, one0 := d.Load(), one.Load()
	res = <-dialSiteAsync(tt, "example.net")
	if res.err != nil || routeIDOf(res.conn) != "two" || d.Load() != d0 || one.Load() != one0 || two.Load() != 2 {
		t.Fatalf("%+v; direct %d->%d, first exit %d->%d", res, d0, d.Load(), one0, one.Load())
	}
	res.conn.Close()
}

func TestRememberedRouteFailingIsForgottenAndTheRaceRuns(t *testing.T) {
	var d, tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, failDialer(&d), echoDialer("t:", &tu))
	clock := useFakeSiteTimer(tt)
	e := tt.sites.match("example.net")
	e.remember(RouteDirect, "", time.Now())
	res := <-dialSiteAsync(tt, "example.net")
	if res.err != nil || res.route != RouteTunnel || d.Load() != 1 || tu.Load() != 1 {
		t.Fatalf("%+v, calls %d %d", res, d.Load(), tu.Load())
	}
	res.conn.Close()
	if len(clock.asked) != 1 {
		t.Fatal("no head start for the remembered route")
	}
	if w, ok := e.winner(time.Now()); !ok || w.route != RouteTunnel {
		t.Fatalf("memory not replaced: %+v %v", w, ok)
	}
}

func TestSiteMemoryExpires(t *testing.T) {
	var d, tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &d), echoDialer("t:", &tu))
	clock := useFakeSiteTimer(tt)
	now := time.Now()
	tt.now = func() time.Time { return now }
	e := tt.sites.match("example.net")
	e.remember(RouteTunnel, "", now)
	now = now.Add(siteWinFor + time.Second)
	res := <-dialSiteAsync(tt, "example.net")
	if res.err != nil || res.route != RouteDirect || tu.Load() != 0 {
		t.Fatalf("expired memory still used: %+v, tunnel calls %d", res, tu.Load())
	}
	res.conn.Close()
	clock.wait(t)
}

func TestNetworkChangeForgetsSiteWinners(t *testing.T) {
	tt := newBrowserTunnel(t, testSites, failDialer(new(atomic.Int32)), echoDialer("t:", new(atomic.Int32)))
	e := tt.sites.match("example.net")
	e.remember(RouteTunnel, "two", time.Now())
	tt.NetworkChanged()
	if _, ok := e.winner(time.Now()); ok {
		t.Fatal("a network change kept the remembered route")
	}
}

func TestSmartSiteTriesItsRememberedRouteFirst(t *testing.T) {
	var d, s atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &d), echoDialer("t:", new(atomic.Int32)))
	tt.ways[RouteSmart] = way{echoDialer("s:", &s), time.Second}
	tt.sites.match("example.org").remember(RouteSmart, "", time.Now())
	res := <-dialSiteAsync(tt, "example.org")
	if res.err != nil || res.route != RouteSmart || d.Load() != 0 {
		t.Fatalf("%+v, direct calls %d", res, d.Load())
	}
	res.conn.Close()
}
