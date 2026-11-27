package tunnel

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"time"
)

// pinnedAttemptLimit bounds one attempt to open a session: a node that takes
// the connection and then stalls must not use the whole deadline.
var pinnedAttemptLimit = 10 * time.Second

const (
	pinnedReturn    = 30 * time.Minute
	pinnedLookupTTL = 5 * time.Minute
	pinnedMaxExtra  = 2
	pinnedDialLimit = 8 * time.Second
	pinnedDoTLimit  = 3 * time.Second
)

// pinnedAddr picks the address a route dials. The pinned address is used
// while it works; after a failed session the host's resolved addresses are
// tried, and the pinned one is tried again after pinnedReturn.
type pinnedAddr struct {
	pinned string
	host   string
	lookup func(ctx context.Context, host string) ([]string, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
	now    func() time.Time
	limit  time.Duration // per dial attempt

	mu       sync.Mutex
	cands    []string // pinned first
	resolved time.Time
	idx      int
	since    time.Time
	httpMode bool                             // pooled HTTP connections must not outlive a switch
	dot      []string                         // DNS-over-TLS servers for the first lookup
	live     map[string]map[*pinConn]struct{} // open connections by address
}

// pinConn removes itself from its pinnedAddr when closed.
type pinConn struct {
	net.Conn
	p    *pinnedAddr
	addr string
	once sync.Once
}

func (c *pinConn) Close() error {
	c.once.Do(func() {
		c.p.mu.Lock()
		delete(c.p.live[c.addr], c)
		c.p.mu.Unlock()
	})
	return c.Conn.Close()
}

func newPinnedAddr(pinned, host string) *pinnedAddr {
	p := &pinnedAddr{
		pinned: pinned,
		host:   host,
		dial:   (&net.Dialer{}).DialContext,
		now:    time.Now,
		limit:  pinnedDialLimit,
		cands:  []string{pinned},
	}
	p.dot = dotServers(nil)
	p.lookup = lookupDoTFirst(func(ctx context.Context, host string) ([]string, error) {
		p.mu.Lock()
		servers := p.dot
		p.mu.Unlock()
		return dotLookup(ctx, host, servers)
	}, net.DefaultResolver.LookupHost)
	return p
}

func (p *pinnedAddr) current() string {
	p.mu.Lock()
	var stale map[*pinConn]struct{}
	if p.idx != 0 && p.now().Sub(p.since) >= pinnedReturn {
		p.idx = 0
		stale = p.takeOthersLocked()
	}
	addr := p.cands[p.idx]
	p.mu.Unlock()
	closeAll(stale)
	return addr
}

// takeOthersLocked removes and returns the pooled connections to every
// address but the current one, in HTTP mode, where a pooled connection is
// reused whatever the dialed address is.
func (p *pinnedAddr) takeOthersLocked() map[*pinConn]struct{} {
	if !p.httpMode {
		return nil
	}
	out := map[*pinConn]struct{}{}
	for addr, conns := range p.live {
		if addr == p.cands[p.idx] {
			continue
		}
		for c := range conns {
			out[c] = struct{}{}
		}
		delete(p.live, addr)
	}
	return out
}

// attemptContext bounds one attempt to open a session by pinnedAttemptLimit,
// and by half of what is left of the caller's deadline, so that a retry
// still has time.
func attemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	limit := pinnedAttemptLimit
	if d, ok := ctx.Deadline(); ok {
		limit = min(limit, time.Until(d)/2)
	}
	return context.WithTimeout(ctx, limit)
}

func (p *pinnedAddr) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	// A dropped SYN must fail here, while the session's own deadline is alive.
	ctx, cancel := context.WithTimeout(ctx, p.limit)
	defer cancel()
	addr := p.current()
	conn, err := p.dial(ctx, network, net.JoinHostPort(addr, port))
	if err != nil {
		return nil, err
	}
	pc := &pinConn{Conn: conn, p: p, addr: addr}
	p.mu.Lock()
	if p.cands[p.idx] != addr { // the route moved on while this dial ran
		p.mu.Unlock()
		conn.Close()
		return nil, errors.New("address no longer in use")
	}
	if p.live == nil {
		p.live = map[string]map[*pinConn]struct{}{}
	}
	if p.live[addr] == nil {
		p.live[addr] = map[*pinConn]struct{}{}
	}
	p.live[addr][pc] = struct{}{}
	p.mu.Unlock()
	return pc, nil
}

// canMove reports whether a failed attempt could still lead to another
// address: one is known, or the lookup has not been tried yet.
func (p *pinnedAddr) canMove() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.idx+1 < len(p.cands) || p.host != "" && net.ParseIP(p.host) == nil && p.now().Sub(p.resolved) >= pinnedLookupTTL
}

// next moves past failed, which must be the address the failed attempt used.
// It reports false when no untried address is left; the next attempt then
// starts again from the pinned one.
func (p *pinnedAddr) next(ctx context.Context, failed string) bool {
	p.mu.Lock()
	if p.cands[p.idx] != failed {
		p.mu.Unlock()
		return true // another session already moved on
	}
	cands := p.cands
	if p.host != "" && net.ParseIP(p.host) == nil && p.now().Sub(p.resolved) >= pinnedLookupTTL {
		p.resolved = p.now()
		p.mu.Unlock()
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err := p.lookup(lctx, p.host)
		cancel()
		p.mu.Lock()
		if p.cands[p.idx] != failed {
			p.mu.Unlock()
			return true
		}
		if err != nil {
			p.resolved = p.now().Add(30*time.Second - pinnedLookupTTL) // try again in 30 s
		}
		if err == nil {
			cands = append([]string{p.pinned}, pickAddrs(addrs, p.pinned, pinnedMaxExtra)...)
			p.cands = cands
			p.idx = max(slices.Index(cands, failed), 0)
		}
	}
	if p.idx+1 >= len(cands) {
		p.idx = 0
		stale := p.takeOthersLocked()
		p.mu.Unlock()
		closeAll(stale)
		return false
	}
	p.idx++
	p.since = p.now()
	stale := p.takeOthersLocked()
	p.mu.Unlock()
	closeAll(stale)
	return true
}

// closeAll drops pooled connections to an address that stopped serving: a
// live HTTP/2 connection would otherwise keep carrying new requests there.
func closeAll(conns map[*pinConn]struct{}) {
	for c := range conns {
		c.Conn.Close()
	}
}

// pickAddrs returns up to n addresses other than skip, IPv4 first.
func pickAddrs(addrs []string, skip string, n int) []string {
	var v4, v6 []string
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || a == skip {
			continue
		}
		if ip.To4() != nil {
			v4 = append(v4, a)
		} else {
			v6 = append(v6, a)
		}
	}
	out := append(v4, v6...)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

type lookupFunc = func(ctx context.Context, host string) ([]string, error)

// builtinDoT answers when the resolver list has no DNS-over-TLS server.
var builtinDoT = []string{"77.88.8.8:853", "77.88.8.1:853"}

const maxDoTServers = 2 // what pinnedDoTLimit allows at 1.5 s each

// dotServers picks the DNS-over-TLS servers (port 853) from a resolver list,
// in its order.
func dotServers(resolvers []string) []string {
	var out []string
	for _, r := range resolvers {
		if isDoT(r) && !slices.Contains(out, r) {
			out = append(out, r)
			if len(out) == maxDoTServers {
				break
			}
		}
	}
	if len(out) == 0 {
		return builtinDoT
	}
	return out
}

// setResolvers takes the DNS-over-TLS servers from the route pool's list.
func (p *pinnedAddr) setResolvers(resolvers []string) {
	p.mu.Lock()
	p.dot = dotServers(resolvers)
	p.mu.Unlock()
}

// dotLookup resolves host through DNS-over-TLS with certificate checks.
func dotLookup(ctx context.Context, host string, servers []string) ([]string, error) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var err error
		for _, server := range servers {
			// Each server gets its share, so a silent one does not use it all.
			sctx, cancel := context.WithTimeout(ctx, max(pinnedDoTLimit/time.Duration(len(servers)), 1500*time.Millisecond))
			var c net.Conn
			c, err = dialResolver(sctx, "tcp", server)
			cancel()
			if err == nil {
				return c, nil
			}
		}
		return nil, err
	}}
	return r.LookupHost(ctx, host)
}

// lookupDoTFirst asks dot for a bounded time, and the system resolver only
// when dot gives no address.
func lookupDoTFirst(dot, system lookupFunc) lookupFunc {
	return func(ctx context.Context, host string) ([]string, error) {
		dctx, cancel := context.WithTimeout(ctx, pinnedDoTLimit)
		addrs, err := dot(dctx, host)
		cancel()
		if err == nil && len(addrs) > 0 {
			return addrs, nil
		}
		// An authenticated "no such name" is an answer: do not ask in clear text.
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, err
		}
		return system(ctx, host)
	}
}
