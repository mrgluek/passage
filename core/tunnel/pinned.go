package tunnel

import (
	"context"
	"net"
	"slices"
	"sync"
	"time"
)

const (
	pinnedReturn    = 30 * time.Minute
	pinnedLookupTTL = 5 * time.Minute
	pinnedMaxExtra  = 2
	pinnedDialLimit = 8 * time.Second
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
	return &pinnedAddr{
		pinned: pinned,
		host:   host,
		lookup: net.DefaultResolver.LookupHost,
		dial:   (&net.Dialer{}).DialContext,
		now:    time.Now,
		limit:  pinnedDialLimit,
		cands:  []string{pinned},
	}
}

func (p *pinnedAddr) current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idx != 0 && p.now().Sub(p.since) >= pinnedReturn {
		p.idx = 0
	}
	return p.cands[p.idx]
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
		if err == nil {
			cands = append([]string{p.pinned}, pickAddrs(addrs, p.pinned, pinnedMaxExtra)...)
			p.cands = cands
			p.idx = max(slices.Index(cands, failed), 0)
		}
	}
	stale := p.live[failed]
	delete(p.live, failed)
	if p.idx+1 >= len(cands) {
		p.idx = 0
		p.mu.Unlock()
		closeAll(stale)
		return false
	}
	p.idx++
	p.since = p.now()
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
