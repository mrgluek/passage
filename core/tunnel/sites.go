package tunnel

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

// SiteGroup lists hosts for browsers and the kind of route that may carry them.
type SiteGroup = links.SiteGroup

// siteBadFor is how long a route that failed or stalled for a site is tried last.
const siteBadFor = 10 * time.Minute

// cleanSites keeps the groups this release understands (smart, tunnel), drops
// invalid hosts and gives every host one kind: a host in both kinds is a
// tunnel host. The result has at most one group per kind, hosts sorted.
func cleanSites(groups []SiteGroup) []SiteGroup {
	kinds := map[string]string{}
	for _, g := range groups {
		if g.Via != links.SiteViaSmart && g.Via != links.SiteViaTunnel {
			continue
		}
		for _, h := range g.Hosts {
			h = strings.ToLower(strings.TrimSuffix(h, "."))
			if links.ValidateSiteHost(h) != nil {
				continue
			}
			if kinds[h] != links.SiteViaTunnel {
				kinds[h] = g.Via
			}
		}
	}
	var out []SiteGroup
	for _, via := range []string{links.SiteViaSmart, links.SiteViaTunnel} {
		var hosts []string
		for h, v := range kinds {
			if v == via {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) > 0 {
			sort.Strings(hosts)
			out = append(out, SiteGroup{Via: via, Hosts: hosts})
		}
	}
	return out
}

func sameSites(a, b []SiteGroup) bool {
	return slices.EqualFunc(a, b, func(x, y SiteGroup) bool { return x.Via == y.Via && slices.Equal(x.Hosts, y.Hosts) })
}

// SiteGroups merges the groups of Config.Sites and every subscription.
func (c Config) SiteGroups() []SiteGroup {
	return cleanSites(c.siteGroupsInOrder())
}

// siteGroupsInOrder is the groups as the subscriptions list them, before
// cleaning; the site numbers follow this order.
func (c Config) siteGroupsInOrder() []SiteGroup {
	all := slices.Clone(c.Sites)
	for _, s := range c.Subscriptions {
		all = append(all, s.Sites...)
	}
	return all
}

// numberSites gives each entry its place in the merged list in
// subscription order, counting each host once. Reports and logs name a
// site by this number, which the subscription's author can map back and
// nobody else can.
func (s *siteSet) numberSites(ordered []SiteGroup) {
	for _, e := range s.entries {
		e.num = 0
	}
	n := 0
	for _, g := range ordered {
		for _, h := range g.Hosts {
			h = strings.ToLower(strings.TrimSuffix(h, "."))
			if e := s.entries[h]; e != nil && e.num == 0 {
				n++
				e.num = n
			}
		}
	}
}

// label names the entry in reports and logs: "site #3 (smart)".
func (e *siteEntry) label() string {
	return "site #" + strconv.Itoa(e.num) + " (" + viaName(e.tunnel) + ")"
}

// siteEntry is one listed host. The routes that failed for it are remembered
// here only: site connections never touch the groups' route state.
type siteEntry struct {
	name   string // the listed host
	num    int    // 1-based place in the merged list, in subscription order
	tunnel bool
	mu     sync.Mutex
	bad    [numRoutes]time.Time
	// badStrategy: smart strategies that got no reply for this site lately.
	badStrategy map[string]time.Time
	win         siteWin // the route that carried the last connection
	// tunnelOnly: a tunnel site whose direct or smart way stalled goes
	// straight to the tunnel until then.
	tunnelOnly time.Time
}

// siteTunnelOnlyFor is how long a tunnel site skips direct and smart after
// an early stall on one of them: the stall repeats on every new connection.
const siteTunnelOnlyFor = 24 * time.Hour

// useTunnelOnly starts the tunnel-only period; it reports whether it was new.
func (e *siteEntry) useTunnelOnly(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	fresh := !now.Before(e.tunnelOnly)
	e.tunnelOnly = now.Add(siteTunnelOnlyFor)
	return fresh
}

func (e *siteEntry) tunnelOnlyNow(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return now.Before(e.tunnelOnly)
}

// siteWinFor is how long the route (and exit) that last carried a site is
// tried first, before the others.
const siteWinFor = 10 * time.Minute

type siteWin struct {
	route    Route
	exit     string // the tunnel route's ID, for the tunnel
	strategy string // the smart strategy, for smart
	until    time.Time
}

// winner returns the remembered route while it is fresh.
func (e *siteEntry) winner(now time.Time) (siteWin, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.win, now.Before(e.win.until)
}

func (e *siteEntry) remember(r Route, exit string, now time.Time, strategy ...string) {
	e.mu.Lock()
	e.win = siteWin{route: r, exit: exit, until: now.Add(siteWinFor)}
	if len(strategy) > 0 {
		e.win.strategy = strategy[0]
	}
	e.mu.Unlock()
}

func (e *siteEntry) strategyBadUntil(name string) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.badStrategy[name]
}

func (e *siteEntry) markStrategyBad(name string, until time.Time) {
	e.mu.Lock()
	if e.badStrategy == nil {
		e.badStrategy = map[string]time.Time{}
	}
	e.badStrategy[name] = until
	if e.win.route == RouteSmart && e.win.strategy == name {
		e.win = siteWin{}
	}
	e.mu.Unlock()
}

func (e *siteEntry) forget() {
	e.mu.Lock()
	e.win = siteWin{}
	e.mu.Unlock()
}

func (e *siteEntry) badUntil(r Route) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bad[r]
}

func (e *siteEntry) markBad(r Route, until time.Time) {
	e.mu.Lock()
	e.bad[r] = until
	if e.win.route == r {
		e.win = siteWin{}
	}
	if r == RouteTunnel {
		e.tunnelOnly = time.Time{} // the tunnel fails too: back to every way
	}
	e.mu.Unlock()
}

func (e *siteEntry) clear(r Route) {
	e.mu.Lock()
	e.bad[r] = time.Time{}
	e.mu.Unlock()
}

type siteSet struct {
	entries map[string]*siteEntry
	hosts   []string // sorted, for the PAC
}

func newSiteSet(groups []SiteGroup) *siteSet {
	s := &siteSet{entries: map[string]*siteEntry{}}
	for _, g := range cleanSites(groups) {
		for _, h := range g.Hosts {
			s.entries[h] = &siteEntry{name: h, tunnel: g.Via == links.SiteViaTunnel}
			s.hosts = append(s.hosts, h)
		}
	}
	sort.Strings(s.hosts)
	s.numberSites(groups)
	return s
}

// resetMemory drops every remembered winner and bad route, after a network
// change: what failed on the old network may work on the new one.
func (s *siteSet) resetMemory() {
	if s == nil {
		return
	}
	for _, e := range s.entries {
		e.mu.Lock()
		e.win, e.bad, e.badStrategy, e.tunnelOnly = siteWin{}, [numRoutes]time.Time{}, nil, time.Time{}
		e.mu.Unlock()
	}
}

// match finds the entry covering host: the host itself or a parent domain,
// never a bare top-level name. One map lookup per label.
func (s *siteSet) match(host string) *siteEntry {
	if s == nil || len(s.entries) == 0 {
		return nil
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(host) > 253 {
		return nil
	}
	for {
		if e := s.entries[host]; e != nil {
			return e
		}
		i := strings.IndexByte(host, '.')
		if i < 0 {
			return nil
		}
		host = host[i+1:]
	}
}
