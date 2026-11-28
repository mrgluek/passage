package exitpolicy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	p       *policy
	lookups atomic.Int32
	dialed  [][]netip.Addr
	answers map[string][]netip.Addr
	delay   time.Duration
}

func newFixture(t *testing.T, rules []Rule) *fixture {
	t.Helper()
	p, err := newPolicy(rules)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{p: p, answers: map[string][]netip.Addr{}}
	p.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		f.lookups.Add(1)
		if f.delay > 0 {
			select {
			case <-time.After(f.delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		a, ok := f.answers[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		return a, nil
	}
	p.connect = func(_ context.Context, a []netip.Addr, _ uint16) (net.Conn, error) {
		f.dialed = append(f.dialed, a)
		c, _ := net.Pipe()
		return c, nil
	}
	return f
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, v := range s {
		out = append(out, netip.MustParseAddr(v))
	}
	return out
}

var rangeRules = []Rule{
	{CIDR: "198.51.100.0/24", Ports: []uint16{443}},
	{Host: ".example.org", Ports: []uint16{443}},
	{Host: "mail.example.net", Ports: []uint16{993}},
}

func TestNameInsideRangeIsDialedAtCheckedAddress(t *testing.T) {
	f := newFixture(t, rangeRules)
	f.answers["web.example.test"] = addrs("198.51.100.99", "198.51.100.41")
	c, st := f.p.dial("Web.Example.Test.", 443)
	if c == nil || st != 0 {
		t.Fatalf("refused: %d", st)
	}
	c.Close()
	if len(f.dialed) != 1 || len(f.dialed[0]) != 2 || f.dialed[0][0].String() != "198.51.100.99" {
		t.Fatalf("dialed %v", f.dialed)
	}
}

func TestMixedOrEmptyAnswerRefused(t *testing.T) {
	f := newFixture(t, rangeRules)
	f.answers["mixed.example.test"] = addrs("198.51.100.99", "203.0.113.34")
	f.answers["empty.example.test"] = nil
	f.answers["v6.example.test"] = addrs("198.51.100.99", "2001:db8::1")
	for _, h := range []string{"mixed.example.test", "empty.example.test", "v6.example.test", "unknown.example.test"} {
		if c, st := f.p.dial(h, 443); c != nil || st != 1 {
			t.Fatalf("%s: status %d", h, st)
		}
	}
	if len(f.dialed) != 0 {
		t.Fatal("dialed a refused name")
	}
	if f.lookups.Load() != 4 {
		t.Fatalf("%d lookups", f.lookups.Load())
	}
}

func TestNameRuleOnAnotherPortUsesRangeCheck(t *testing.T) {
	f := newFixture(t, []Rule{{Host: "mail.example.net", Ports: []uint16{993}}, {CIDR: "198.51.100.0/24", Ports: []uint16{443}}})
	f.answers["mail.example.net"] = addrs("203.0.113.34")
	if c, st := f.p.dial("mail.example.net", 443); c != nil || st != 1 || f.lookups.Load() != 1 {
		t.Fatalf("status %d, %d lookups", st, f.lookups.Load())
	}
	f.p.cache = map[string]entry{}
	f.answers["mail.example.net"] = addrs("198.51.100.9")
	c, st := f.p.dial("mail.example.net", 443)
	if c == nil || st != 0 {
		t.Fatalf("status %d", st)
	}
	c.Close()
}

func TestRangeIsPerPort(t *testing.T) {
	f := newFixture(t, rangeRules)
	f.answers["web.example.test"] = addrs("198.51.100.99")
	if c, st := f.p.dial("web.example.test", 80); c != nil || st != 1 {
		t.Fatal("other port allowed")
	}
	if f.lookups.Load() != 0 {
		t.Fatal("lookup for a port without CIDR rule")
	}
}

func TestNameRuleWinsWithoutRangeLookup(t *testing.T) {
	f := newFixture(t, rangeRules)
	f.answers["a.example.org"] = addrs("203.0.113.34")
	c, st := f.p.dial("a.example.org", 443)
	if c == nil || st != 0 {
		t.Fatalf("status %d", st)
	}
	c.Close()
	// A name-rule host resolving into private space stays refused.
	f.answers["b.example.org"] = addrs("10.0.0.1")
	if c, st := f.p.dial("b.example.org", 443); c != nil || st != 1 {
		t.Fatal("private address allowed")
	}
	if len(f.p.cache) != 0 {
		t.Fatal("name rule path used the range cache")
	}
}

func TestPrivateAddressInsideRuleRefused(t *testing.T) {
	f := newFixture(t, []Rule{{CIDR: "10.0.0.0/16", Ports: []uint16{443}}})
	f.answers["internal.example.com"] = addrs("10.0.0.5")
	if c, st := f.p.dial("internal.example.com", 443); c != nil || st != 1 {
		t.Fatal("private range allowed")
	}
}

func TestCacheAvoidsSecondLookup(t *testing.T) {
	f := newFixture(t, rangeRules)
	now := time.Unix(1000, 0)
	f.p.now = func() time.Time { return now }
	f.answers["web.example.test"] = addrs("198.51.100.99")
	for i := 0; i < 3; i++ {
		c, st := f.p.dial("web.example.test", 443)
		if c == nil || st != 0 {
			t.Fatal("refused")
		}
		c.Close()
	}
	if f.lookups.Load() != 1 {
		t.Fatalf("%d lookups", f.lookups.Load())
	}
	now = now.Add(cacheTTL + time.Second)
	f.answers["web.example.test"] = addrs("203.0.113.34")
	if c, st := f.p.dial("web.example.test", 443); c != nil || st != 1 {
		t.Fatal("stale answer used")
	}
	if f.lookups.Load() != 2 {
		t.Fatal("expired entry not refreshed")
	}
}

func TestRefusedAnswerIsCachedBriefly(t *testing.T) {
	f := newFixture(t, rangeRules)
	f.answers["bad.example.test"] = addrs("203.0.113.34")
	for i := 0; i < 5; i++ {
		f.p.dial("bad.example.test", 443)
	}
	f.p.dial("nx.example.test", 443)
	f.p.dial("nx.example.test", 443)
	if f.lookups.Load() != 2 {
		t.Fatalf("%d lookups", f.lookups.Load())
	}
}

func TestLookupTimeoutRefusedWithinBound(t *testing.T) {
	f := newFixture(t, rangeRules)
	f.delay = time.Minute
	start := time.Now()
	c, st := f.p.dial("slow.example.test", 443)
	if c != nil || st != 1 {
		t.Fatalf("status %d", st)
	}
	if d := time.Since(start); d > cidrLookupTimeout+time.Second {
		t.Fatalf("took %v", d)
	}
	if f.lookups.Load() != 1 || len(f.dialed) != 0 {
		t.Fatal("expected one lookup and no dial")
	}
}

func TestLookAlikeNamesDoNotMatch(t *testing.T) {
	f := newFixture(t, rangeRules)
	for _, h := range []string{"example.org.evil.net", "notexample.org", "mail.example.net.evil", "xmail.example.net"} {
		f.answers[h] = addrs("203.0.113.34")
		if c, st := f.p.dial(h, 443); c != nil || st != 1 {
			t.Fatalf("%s allowed", h)
		}
	}
	if c, st := f.p.dial("mail.example.net", 25); c != nil || st != 1 {
		t.Fatal("port 25 allowed")
	}
}

func TestLiteralsAsBefore(t *testing.T) {
	f := newFixture(t, rangeRules)
	f.answers["198.51.100.41"] = addrs("198.51.100.41")
	c, st := f.p.dial("198.51.100.41", 443)
	if c == nil || st != 0 {
		t.Fatalf("status %d", st)
	}
	c.Close()
	for _, h := range []string{"127.0.0.1", "203.0.113.34", "::1"} {
		if c, st := f.p.dial(h, 443); c != nil || st != 1 {
			t.Fatalf("%s allowed", h)
		}
	}
}

func TestCacheIsBounded(t *testing.T) {
	f := newFixture(t, rangeRules)
	for i := 0; i < cacheSize*2; i++ {
		f.p.store(string(rune('a'+i%26))+time.Duration(i).String(), nil, time.Hour)
	}
	if len(f.p.cache) > cacheSize {
		t.Fatalf("%d entries", len(f.p.cache))
	}
}

func TestLookupSlotsBounded(t *testing.T) {
	f := newFixture(t, rangeRules)
	for i := 0; i < maxLookups; i++ {
		f.p.slots <- struct{}{}
	}
	if c, st := f.p.dial("web.example.test", 443); c != nil || st != 1 || f.lookups.Load() != 0 {
		t.Fatal("lookup ran without a free slot")
	}
}

func TestValidation(t *testing.T) {
	for _, rules := range [][]Rule{nil, {{CIDR: "0.0.0.0/0", Ports: []uint16{443}}}, {{Host: "mail.example.org", Ports: []uint16{25}}}, {{Host: "mail.example.org", CIDR: "192.0.2.0/24", Ports: []uint16{443}}}} {
		if _, err := New(rules); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	for _, s := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::ffff:192.168.1.1"} {
		if Public(netip.MustParseAddr(s)) {
			t.Fatalf("%s is public", s)
		}
	}
}
