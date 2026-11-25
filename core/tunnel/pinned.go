package tunnel

import (
	"context"
	"net"
	"sync"
	"time"
)

const (
	pinnedReturn    = 30 * time.Minute
	pinnedLookupTTL = 5 * time.Minute
	pinnedMaxExtra  = 2
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

	mu       sync.Mutex
	cands    []string // pinned first
	resolved time.Time
	idx      int
	since    time.Time
}

func newPinnedAddr(pinned, host string) *pinnedAddr {
	return &pinnedAddr{
		pinned: pinned,
		host:   host,
		lookup: net.DefaultResolver.LookupHost,
		dial:   (&net.Dialer{}).DialContext,
		now:    time.Now,
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
	return p.dial(ctx, network, net.JoinHostPort(p.current(), port))
}

// next moves past failed, which must be the address the failed attempt used.
// It reports false when no untried address is left; the next attempt then
// starts again from the pinned one.
func (p *pinnedAddr) next(ctx context.Context, failed string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cands[p.idx] != failed {
		return true // another session already moved on
	}
	if len(p.cands) == 1 && p.host != "" && net.ParseIP(p.host) == nil && p.now().Sub(p.resolved) >= pinnedLookupTTL {
		p.resolved = p.now()
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err := p.lookup(lctx, p.host)
		cancel()
		if err == nil {
			p.cands = append(p.cands, pickAddrs(addrs, p.pinned, pinnedMaxExtra)...)
		}
	}
	if p.idx+1 >= len(p.cands) {
		p.idx = 0
		return false
	}
	p.idx++
	p.since = p.now()
	return true
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
