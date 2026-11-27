// Package exitpolicy decides which destinations a tunnel exit may dial.
package exitpolicy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/mrgluek/passage/core/internal/exitdial"
)

// Rule allows a host name, a ".suffix" or a CIDR range on the listed ports.
type Rule struct {
	Host  string   `json:"host,omitempty"`
	CIDR  string   `json:"cidr,omitempty"`
	Ports []uint16 `json:"ports"`
}

const (
	nameLookupTimeout = 8 * time.Second
	cidrLookupTimeout = 2 * time.Second
	dialTimeout       = 8 * time.Second
	cacheTTL          = 2 * time.Minute
	refusedTTL        = 5 * time.Second
	cacheSize         = 1024
	maxLookups        = 32
)

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Public reports whether ip is a global unicast address outside private ranges.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !cgnat.Contains(ip)
}

type entry struct {
	addrs   []netip.Addr
	expires time.Time
}

type policy struct {
	rules    []Rule
	prefixes []netip.Prefix
	lookup   func(context.Context, string) ([]netip.Addr, error)
	connect  func(context.Context, []netip.Addr, uint16) (net.Conn, error)
	now      func() time.Time
	slots    chan struct{}
	mu       sync.Mutex
	cache    map[string]entry
}

// New validates rules and returns the dial function for the exits.
// The status is 0 on success, 1 when refused and 2 when the dial failed.
func New(rules []Rule) (func(string, uint16) (net.Conn, byte), error) {
	p, err := newPolicy(rules)
	if err != nil {
		return nil, err
	}
	p.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	p.connect = exitdial.Dial
	return p.dial, nil
}

func newPolicy(rules []Rule) (*policy, error) {
	if len(rules) == 0 || len(rules) > 256 {
		return nil, errors.New("configure allowed destinations")
	}
	prefixes := make([]netip.Prefix, len(rules))
	for i, r := range rules {
		if (r.Host == "") == (r.CIDR == "") || len(r.Ports) == 0 {
			return nil, errors.New("invalid destination rule")
		}
		if r.CIDR != "" {
			p, err := netip.ParsePrefix(r.CIDR)
			if err != nil || p.Bits() < 16 || p.Addr().Is6() && p.Bits() < 32 {
				return nil, errors.New("invalid destination range")
			}
			prefixes[i] = p
		}
		for _, p := range r.Ports {
			if p == 0 || p == 25 {
				return nil, errors.New("invalid destination port")
			}
		}
	}
	return &policy{rules: rules, prefixes: prefixes, now: time.Now, slots: make(chan struct{}, maxLookups), cache: map[string]entry{}}, nil
}

func hasPort(ports []uint16, port uint16) bool {
	for _, p := range ports {
		if p == port {
			return true
		}
	}
	return false
}

func (p *policy) dial(host string, port uint16) (net.Conn, byte) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	ip, _ := netip.ParseAddr(host)
	nameOK, literalOK, cidrPort := false, false, false
	for i, r := range p.rules {
		if !hasPort(r.Ports, port) {
			continue
		}
		if r.CIDR != "" {
			cidrPort = true
			literalOK = literalOK || ip.IsValid() && p.prefixes[i].Contains(ip)
			continue
		}
		name := strings.TrimSuffix(strings.ToLower(r.Host), ".")
		if strings.HasPrefix(name, ".") {
			nameOK = nameOK || host == name[1:] || strings.HasSuffix(host, name)
		} else {
			nameOK = nameOK || name != "" && host == name
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	var addrs []netip.Addr
	switch {
	case nameOK || literalOK:
		var err error
		addrs, err = p.lookup(ctx, host)
		if err != nil {
			return nil, 2
		}
		for _, a := range addrs {
			if !Public(a) {
				return nil, 1
			}
		}
	case cidrPort && !ip.IsValid():
		addrs = p.resolveInRange(ctx, host, port)
		if addrs == nil {
			return nil, 1
		}
	default:
		return nil, 1
	}
	// Dial only the checked addresses; the name is never resolved again.
	conn, err := p.connect(ctx, addrs, port)
	if err != nil {
		return nil, 2
	}
	return conn, 0
}

// resolveInRange returns the addresses of host when every one is public and
// inside a CIDR rule for port, or nil. Answers are cached for a short time.
func (p *policy) resolveInRange(ctx context.Context, host string, port uint16) []netip.Addr {
	if addrs, ok := p.cached(host); ok {
		return p.inRange(addrs, port)
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, cidrLookupTimeout)
	defer cancel()
	addrs, err := p.lookup(lctx, host)
	if err != nil || lctx.Err() != nil || len(addrs) == 0 {
		p.store(host, nil, refusedTTL)
		return nil
	}
	p.store(host, addrs, cacheTTL)
	return p.inRange(addrs, port)
}

func (p *policy) cached(host string) ([]netip.Addr, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.cache[host]
	if !ok || !p.now().Before(e.expires) {
		delete(p.cache, host)
		return nil, false
	}
	return e.addrs, true
}

func (p *policy) store(host string, addrs []netip.Addr, ttl time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if _, ok := p.cache[host]; !ok && len(p.cache) >= cacheSize {
		for k, e := range p.cache {
			if !now.Before(e.expires) {
				delete(p.cache, k)
			}
		}
	}
	for k := range p.cache {
		if _, ok := p.cache[host]; ok || len(p.cache) < cacheSize {
			break
		}
		delete(p.cache, k)
	}
	p.cache[host] = entry{addrs: addrs, expires: now.Add(ttl)}
}

func (p *policy) inRange(addrs []netip.Addr, port uint16) []netip.Addr {
	if len(addrs) == 0 || len(addrs) > 64 {
		return nil
	}
	for _, a := range addrs {
		a = a.Unmap()
		ok := false
		for i, r := range p.rules {
			if r.CIDR != "" && hasPort(r.Ports, port) && p.prefixes[i].Contains(a) {
				ok = true
				break
			}
		}
		if !ok || !Public(a) {
			return nil
		}
	}
	return addrs
}
