package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Listed sites' names are resolved over DNS-over-TLS for direct and smart
// attempts, so the network's resolver neither sees them nor can answer with
// a wrong address. The tunnel's exit resolves on its own.
const (
	siteDNSFor     = 2 * time.Minute  // a found answer (the TTL is not available here)
	siteDNSFailFor = 30 * time.Second // no answer: the next connection does not wait again
	siteDNSMax     = 512
)

// siteAddrMin is the least time one of several addresses gets.
const siteAddrMin = time.Second

// errSiteDoT: DoT gave no answer for a listed site.
var errSiteDoT = errors.New("DoT lookup failed")

type siteAnswer struct {
	addrs []string
	err   error
	until time.Time
}

type siteLookup struct {
	done  chan struct{}
	addrs []string
	err   error
}

type siteDNS struct {
	dot    lookupFunc
	system lookupFunc
	now    func() time.Time

	mu     sync.Mutex
	cache  map[string]siteAnswer
	flight map[string]*siteLookup
}

func newSiteDNS(resolvers []string, now func() time.Time) *siteDNS {
	servers := dotServers(resolvers)
	return &siteDNS{
		dot:    func(ctx context.Context, host string) ([]string, error) { return dotLookup(ctx, host, servers) },
		system: net.DefaultResolver.LookupHost,
		now:    now,
	}
}

// lookup resolves host over DoT. Without an answer, fallback allows the
// system resolver, except after an authenticated "no such name".
func (d *siteDNS) lookup(ctx context.Context, host string, fallback bool) ([]string, error) {
	addrs, err := d.dotCached(ctx, host)
	if err == nil {
		return addrs, nil
	}
	var dnsErr *net.DNSError
	if !fallback || errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, fmt.Errorf("%w: %w", errSiteDoT, err)
	}
	return d.system(ctx, host)
}

func (d *siteDNS) dotCached(ctx context.Context, host string) ([]string, error) {
	d.mu.Lock()
	now := d.now()
	if a, ok := d.cache[host]; ok && now.Before(a.until) {
		d.mu.Unlock()
		return a.addrs, a.err
	}
	call, ok := d.flight[host]
	if !ok {
		call = &siteLookup{done: make(chan struct{})}
		if d.flight == nil {
			d.flight = map[string]*siteLookup{}
		}
		d.flight[host] = call
		go d.run(host, call)
	}
	d.mu.Unlock()
	select {
	case <-call.done:
		return call.addrs, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// run does one lookup for everyone waiting on host. It has its own deadline,
// so a waiter that gives up does not cancel it for the others.
func (d *siteDNS) run(host string, call *siteLookup) {
	ctx, cancel := context.WithTimeout(context.Background(), pinnedDoTLimit)
	addrs, err := d.dot(ctx, host)
	cancel()
	if err == nil && len(addrs) == 0 {
		err = errors.New("no addresses")
	}
	call.addrs, call.err = addrs, err
	d.mu.Lock()
	ttl := siteDNSFor
	if err != nil {
		ttl = siteDNSFailFor
	}
	if d.cache == nil || len(d.cache) >= siteDNSMax {
		d.cache = map[string]siteAnswer{}
	}
	d.cache[host] = siteAnswer{addrs, err, d.now().Add(ttl)}
	delete(d.flight, host)
	d.mu.Unlock()
	close(call.done)
}

func (t *Tunnel) siteResolver() *siteDNS {
	t.siteDNSOnce.Do(func() {
		if t.siteDNS == nil {
			t.siteDNS = newSiteDNS(t.cfg.Effective(), t.timeNow)
		}
	})
	return t.siteDNS
}

// resolvedSiteDial wraps a direct or smart dial: the name is resolved over
// DoT and the addresses are dialed in turn. The browser does TLS itself, so
// its SNI and Host stay as they are.
func (t *Tunnel) resolvedSiteDial(dial dialFunc, fallback bool) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || net.ParseIP(host) != nil {
			return dial(ctx, network, addr)
		}
		addrs, err := t.siteResolver().lookup(ctx, host, fallback)
		if err != nil {
			return nil, err
		}
		var last error
		list := addrsIPv4First(addrs)
		for i, ip := range list {
			// Each address gets its share of the time left, so one that
			// never answers does not use it all; the last gets the rest.
			actx, cancel := ctx, context.CancelFunc(func() {})
			if dl, ok := ctx.Deadline(); ok && i < len(list)-1 {
				actx, cancel = context.WithTimeout(ctx, max(time.Until(dl)/time.Duration(len(list)-i), siteAddrMin))
			}
			c, err := dial(actx, network, net.JoinHostPort(ip, port))
			cancel()
			if err == nil {
				return c, nil
			}
			last = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, last
	}
}

// addrsIPv4First keeps the order within each family, IPv4 first: many networks
// have no working IPv6, and a hanging attempt would use the route's time.
func addrsIPv4First(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, v6 := range []bool{false, true} {
		for _, a := range addrs {
			if ip := net.ParseIP(a); ip != nil && (ip.To4() == nil) == v6 {
				out = append(out, a)
			}
		}
	}
	return out
}
