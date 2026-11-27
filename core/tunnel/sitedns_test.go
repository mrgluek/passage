package tunnel

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countedLookup struct {
	calls atomic.Int32
	fn    lookupFunc
}

func (c *countedLookup) lookup(ctx context.Context, host string) ([]string, error) {
	c.calls.Add(1)
	return c.fn(ctx, host)
}

// fakeSiteDNS answers 192.0.2.1 by default; dot or system replace a side.
func fakeSiteDNS(dot, system lookupFunc) *siteDNS {
	ok := func(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil }
	if dot == nil {
		dot = ok
	}
	if system == nil {
		system = func(context.Context, string) ([]string, error) { return nil, errors.New("system resolver used") }
	}
	return &siteDNS{dot: dot, system: system, now: time.Now}
}

func dotDown(context.Context, string) ([]string, error) { return nil, errors.New("i/o timeout") }

// addrDialer records the addresses it is asked for and connects.
func addrDialer(mu *sync.Mutex, got *[]string) dialFunc {
	return func(_ context.Context, _, addr string) (net.Conn, error) {
		mu.Lock()
		*got = append(*got, addr)
		mu.Unlock()
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
	}
}

func TestTunnelSiteDialsTheDoTAddressNotTheName(t *testing.T) {
	var mu sync.Mutex
	var direct, tunnel []string
	tt := newBrowserTunnel(t, testSites, addrDialer(&mu, &direct), addrDialer(&mu, &tunnel))
	sys := &countedLookup{fn: func(context.Context, string) ([]string, error) { return []string{"198.51.100.1"}, nil }}
	tt.siteDNS = fakeSiteDNS(nil, sys.lookup)
	res := <-dialSiteAsync(tt, "example.net")
	if res.err != nil || res.route != RouteDirect {
		t.Fatalf("%+v", res)
	}
	res.conn.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(direct) != 1 || direct[0] != "192.0.2.1:443" || sys.calls.Load() != 0 {
		t.Fatalf("direct dialed %v, system lookups %d", direct, sys.calls.Load())
	}
}

func TestTunnelSiteWithoutDoTGoesToTheTunnelAtOnce(t *testing.T) {
	var d, tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &d), echoDialer("t:", &tu))
	sys := &countedLookup{fn: func(context.Context, string) ([]string, error) { return []string{"198.51.100.1"}, nil }}
	tt.siteDNS = fakeSiteDNS(dotDown, sys.lookup)
	clock := useFakeSiteTimer(tt)
	res := <-dialSiteAsync(tt, "example.net") // the timer never fires
	if res.err != nil || res.route != RouteTunnel || d.Load() != 0 || sys.calls.Load() != 0 {
		t.Fatalf("%+v; direct dials %d, system lookups %d", res, d.Load(), sys.calls.Load())
	}
	res.conn.Close()
	clock.wait(t)
	if tt.sites.match("example.net").badUntil(RouteDirect).After(time.Now()) {
		t.Fatal("a DoT failure marked the direct route bad")
	}
}

func TestSmartSiteFallsBackToTheSystemResolver(t *testing.T) {
	var mu sync.Mutex
	var direct []string
	tt := newBrowserTunnel(t, testSites, addrDialer(&mu, &direct), echoDialer("t:", new(atomic.Int32)))
	tt.siteDNS = fakeSiteDNS(dotDown, func(context.Context, string) ([]string, error) { return []string{"198.51.100.1"}, nil })
	res := <-dialSiteAsync(tt, "example.org")
	if res.err != nil || res.route != RouteDirect {
		t.Fatalf("%+v", res)
	}
	res.conn.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(direct) != 1 || direct[0] != "198.51.100.1:443" {
		t.Fatalf("%v", direct)
	}
}

func TestSiteDoTAnswerIsCached(t *testing.T) {
	dot := &countedLookup{fn: func(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil }}
	var mu sync.Mutex
	var direct []string
	tt := newBrowserTunnel(t, testSites, addrDialer(&mu, &direct), echoDialer("t:", new(atomic.Int32)))
	tt.siteDNS = fakeSiteDNS(dot.lookup, nil)
	for range 3 {
		res := <-dialSiteAsync(tt, "example.net")
		if res.err != nil {
			t.Fatal(res.err)
		}
		res.conn.Close()
	}
	if dot.calls.Load() != 1 {
		t.Fatalf("%d DoT lookups", dot.calls.Load())
	}
}

func TestSiteDoTOneLookupInFlight(t *testing.T) {
	release := make(chan struct{})
	dot := &countedLookup{fn: func(context.Context, string) ([]string, error) { <-release; return []string{"192.0.2.1"}, nil }}
	d := fakeSiteDNS(dot.lookup, nil)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); d.lookup(context.Background(), "example.net", false) }()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if dot.calls.Load() != 1 {
		t.Fatalf("%d lookups in flight", dot.calls.Load())
	}
}

func TestDoTFailureHasItsOwnLogStage(t *testing.T) {
	if got := siteFailStage(errSiteDoTFor(errors.New("timeout"))); got != "dot" {
		t.Fatal(got)
	}
}

func errSiteDoTFor(err error) error { return errors.Join(errSiteDoT, err) }

func TestSiteAddressesIPv4First(t *testing.T) {
	got := addrsIPv4First([]string{"2001:db8::1", "192.0.2.1", "2001:db8::2", "192.0.2.2"})
	if len(got) != 4 || got[0] != "192.0.2.1" || got[1] != "192.0.2.2" || got[2] != "2001:db8::1" {
		t.Fatal(got)
	}
}
