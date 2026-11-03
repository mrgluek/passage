package tunnel

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/soar"
)

// Route is one way to open connections. Routes are ordered from the most
// direct to the last resort; in auto mode a connection that fails on one
// route falls through to the next.
type Route int

const (
	RouteDirect Route = iota
	RouteSmart        // direct, with the first bytes reshaped (smart.go)
	RouteTunnel
	numRoutes
)

var routeNames = [numRoutes]string{"direct", "smart", "tunnel"}

func (r Route) String() string { return routeNames[r] }

// groupKind says what a group holds and how it is probed.
type groupKind int

const (
	kindRelay    groupKind = iota // one mail relay, by host name: TLS probes to it
	kindTelegram                  // IP literals in Telegram's ranges: MTProto probes
	kindOther                     // other IP literals: not probed
)

var kindNames = [...]string{"relay", "telegram", "other"}

func (k groupKind) String() string { return kindNames[k] }

// group is a set of destinations that share one route decision: each mail
// relay has its own, so one relay being blocked doesn't move another, and
// Telegram has one.
type group struct {
	name        string
	kind        groupKind
	pinned      bool      // from cfg.ProbeTargets: never dropped
	used        time.Time // last CONNECT, for dropping the least recently used relay
	route       Route
	routeSince  time.Time
	via         string               // route type of the last tunnel connection: iroh, soar
	tunnelFails int                  // tunnel dial failures since the route last changed
	hosts       map[string]time.Time // probe host -> last seen in a CONNECT
	ports       map[string]string    // probe host -> port to probe
	seen        bool                 // has carried traffic; only then is it probed
	failStreak  int
	okStreak    int
	state       string
	lastProbe   time.Time
	lastResult  probeResult
}

// viaLocked is the route type behind the tunnel while the group uses it.
func (g *group) viaLocked() string {
	if g.route != RouteTunnel {
		return ""
	}
	return g.via
}

func newGroup(name string, kind groupKind, route Route) *group {
	return &group{name: name, kind: kind, route: route, routeSince: time.Now(),
		hosts: map[string]time.Time{}, ports: map[string]string{}, state: "unknown"}
}

const (
	// maxTelegramHosts bounds the destinations remembered for the Telegram group.
	maxTelegramHosts = 32
	// maxRelayGroups bounds the relays tracked at once; the least recently
	// used one without connections goes first.
	maxRelayGroups = 8
)

// way is how a route dials; a nil dial means the route isn't set up here.
type way struct {
	dial    dialFunc
	timeout time.Duration
}

// soar tuning: query names start short and soar grows them to the 253
// maximum; a wider per-path window. Config.SafeQName overrides the start.
const (
	soarSafeQName  = 101
	soarPathWindow = 64
)

// Timeouts and the monitor's hysteresis.
const (
	handshakeTimeout      = 30 * time.Second
	maxConnections        = 256
	maxPendingHandshakes  = 32
	directDialTimeout     = 8 * time.Second
	tunnelDialTimeout     = 60 * time.Second
	probeTimeout          = 6 * time.Second
	tunnelProbeTimeout    = 30 * time.Second // a cold soar session needs its hellos first
	minProbeGap           = 10 * time.Second
	blockedAfter          = 2 // consecutive probes showing direct access blocked before switching
	recoveredAfter        = 3 // consecutive "a relay answers directly" probes before switching back
	maxProbedRelays       = 4
	suspiciousLifetime    = 20 * time.Second
	interceptProbeTimeout = 800 * time.Millisecond
	suspiciousBytes       = 512
	// canaryStagger is how long one canary gets before the next one starts
	// alongside it (they run one at a time; see probeCanaries).
	canaryStagger = 2 * time.Second
)

// firstResponseTimeout: a connection that has sent bytes and got none back
// for this long triggers a probe (it stays open). A var for tests.
var firstResponseTimeout = 5 * time.Second

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Tunnel is a running local proxy.
type Tunnel struct {
	cfg       Config
	resolvers []string // the paths handed to soar (IPv6 dropped without a route)
	ln        net.Listener
	client    soarClient
	log       *eventLog
	ctx       context.Context
	cancel    context.CancelFunc
	kick      chan struct{}
	wg        sync.WaitGroup

	// Swappable for tests. ways[RouteSmart] is set while a strategy works;
	// guarded by mu.
	ways       [numRoutes]way
	strategies []strategy
	smartProbe func(ctx context.Context, dial dialFunc, hostport, sni string) error // nil: probeTLSVia
	probeTLS   func(ctx context.Context, hostport, sni string) error
	// probeTunnel is probeTLS through the tunnel. established reports a live
	// soar session: only then does a relay failing through the tunnel prove
	// the relay is down (see confirmViaTunnel).
	probeTunnel func(ctx context.Context, hostport, sni string) error
	established func() bool
	// The Telegram group's probes (telegram.go), directly and through the tunnel.
	probeMT       func(ctx context.Context, hostport string) error
	probeMTTunnel func(ctx context.Context, hostport string) error
	ipv6Route     func() bool // nil: hasIPv6Route

	mu          sync.Mutex
	mode        string
	only        string            // route type the tunnel is limited to, with mode always
	relays      map[string]*group // by host name
	telegram    *group
	other       *group
	routeGen    atomic.Int64      // bumped on every route change, for the shell to nudge the core
	urgent      atomic.Bool       // the next probe skips minProbeGap (Recheck)
	intercepted bool              // this network redirects port-53 traffic (see intercept.go)
	dnsEgress   string            // who actually resolves for the interceptor, if learned
	clients     map[net.Conn]bool // true until the SOCKS handshake finishes
	pending     int
	conns       map[*tracked]struct{}
	lastProbe   time.Time // the monitor's last round
	lastCanary  string    // the canary that answered last; tried first next time
	strategy    *strategy // the smart route's working strategy, if any
	stats       [numRoutes]routeStats
	// Bytes per route, counted as they flow: IMAP IDLE connections live for
	// hours, so totals taken at close would show nothing for them.
	upBytes   [numRoutes]atomic.Int64
	downBytes [numRoutes]atomic.Int64
}

type tracked struct {
	group  *group
	route  Route
	local  net.Conn
	remote net.Conn
}

type routeStats struct {
	Conns int64 `json:"conns"`
	Fails int64 `json:"fails"`
	Up    int64 `json:"up"`
	Down  int64 `json:"down"`
}

type probeResult struct {
	Relays    int  `json:"relays"`
	RelayOK   int  `json:"relay_ok"`
	NetworkOK bool `json:"network_ok"` // a target or canary answered directly
	CanaryOK  bool `json:"canary_ok"`
	// Canaries are needed only when no target answered directly.
	CanaryChecked bool `json:"canary_checked,omitempty"`
	// SmartChecked: the probe hosts were tried on the smart route (they
	// failed directly while the network was reachable); SmartOK: a strategy works.
	SmartChecked bool `json:"smart_checked,omitempty"`
	SmartOK      bool `json:"smart_ok,omitempty"`
	// SmartKnown: the strategy had worked on this network before this probe,
	// so one blocked probe is enough to switch to it.
	SmartKnown bool   `json:"smart_known,omitempty"`
	Strategy   string `json:"strategy,omitempty"`
	// Set by confirmViaTunnel: the same targets checked through the tunnel.
	TunnelChecked bool `json:"tunnel_checked,omitempty"`
	TunnelOK      bool `json:"tunnel_ok,omitempty"`

	hosts []string // the host:ports probed, most recent first
}

// Start validates cfg and starts serving SOCKS5. The tunnel starts on demand.
func Start(cfg Config) (*Tunnel, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	mode := cfg.Mode
	if slices.Contains(RouteKinds, mode) {
		mode = ModeAlways
	}
	t := &Tunnel{
		cfg:    cfg,
		log:    newEventLog(500),
		kick:   make(chan struct{}, 1),
		mode:   mode,
		conns:  map[*tracked]struct{}{},
		relays: map[string]*group{},
	}
	t.telegram = newGroup("telegram", kindTelegram, initialRoute(mode))
	t.other = newGroup("other", kindOther, initialRoute(mode))
	t.ctx, t.cancel = context.WithCancel(context.Background())
	var skipped6, skippedPrivate int
	if cfg.Zone != "" {
		key, _ := base64.StdEncoding.DecodeString(cfg.Key)
		var resolvers []string
		resolvers, skippedPrivate = withoutForeignPrivate(cfg.Effective(), cfg.SystemResolvers)
		resolvers = ipv4First(resolvers)
		// On a network that redirects port 53, lead with the DoT paths.
		t.intercepted = detectInterception(context.Background(), interceptProbeTimeout)
		if t.intercepted {
			resolvers = dotFirst(resolvers)
		}
		if !t.hasIPv6Route() {
			// Without an IPv6 route these paths can only fail. A network change
			// restarts the tunnel and re-checks.
			resolvers, skipped6 = withoutIPv6(resolvers)
		}
		t.client = newLazySoar(soar.Config{
			Zone:          cfg.Zone,
			ServerKey:     ed25519.PublicKey(key),
			Resolvers:     resolvers,
			TCP:           "auto",
			SafeQName:     cmp.Or(cfg.SafeQName, soarSafeQName),
			MaxPathWindow: soarPathWindow,
			DialTCP:       dialResolver,
			StreamOnly:    isDoT, // DoT resolvers get their TCP (TLS) path at once, no doomed UDP probe
			Logger:        slog.New(&logHandler{log: t.log}),
		})
		t.resolvers = resolvers
	}
	if cfg.My != "" && len(cfg.Routes) == 0 && cfg.Zone != "" {
		// A v1 link's soar route joins My in the pool.
		cfg.Routes = legacyRoutes(cfg)
	}
	if len(cfg.Routes) > 0 || cfg.My != "" {
		t.client, err = newRoutePool(cfg)
		if err != nil {
			return nil, err
		}
	}
	if mode != cfg.Mode && !t.restrict(cfg.Mode) {
		// The chosen route type is not in the link (any more): back to auto.
		t.mode = ModeAuto
		t.telegram = newGroup("telegram", kindTelegram, RouteDirect)
		t.other = newGroup("other", kindOther, RouteDirect)
	}
	d := &net.Dialer{Timeout: directDialTimeout}
	t.ways[RouteDirect] = way{d.DialContext, directDialTimeout}
	t.strategies = smartStrategies()
	t.useKnownStrategy()
	t.probeTLS = probeTLS
	t.probeMT = probeMTProto
	if t.client != nil {
		t.ways[RouteTunnel] = way{t.client.DialContext, tunnelDialTimeout}
		t.probeTunnel = func(ctx context.Context, hostport, sni string) error {
			return probeTLSVia(ctx, t.ways[RouteTunnel].dial, hostport, sni)
		}
		t.probeMTTunnel = func(ctx context.Context, hostport string) error {
			return probeMTProtoVia(ctx, t.ways[RouteTunnel].dial, hostport)
		}
		t.established = t.client.Established
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		if t.client != nil {
			t.client.Close()
		}
		return nil, err
	}
	if cfg.My != "" && pointsAt(cfg.My, ln.Addr()) {
		ln.Close()
		t.client.Close()
		return nil, errors.New("my route points at this proxy")
	}
	t.ln = ln
	t.useProbeTargets()
	if t.client == nil {
		t.log.add("started: mode=%s, no tunnel (no link)", cfg.Mode)
	} else {
		t.log.add("started: mode=%s resolvers=%d (network %d, operator %s +%d) soar=%s", cfg.Mode, len(t.resolvers),
			len(cfg.SystemResolvers), cfg.Operator, len(OperatorResolvers[cfg.Operator]), SoarVersion())
	}
	if skipped6 > 0 {
		t.log.add("no IPv6 route: skipped %d IPv6 resolvers", skipped6)
	}
	if t.intercepted {
		t.log.add("this network intercepts DNS (port 53): plain-DNS paths all reach one resolver; DoT paths moved first")
		go t.learnDNSEgress()
	}
	if skippedPrivate > 0 {
		t.log.add("skipped %d private-address resolvers this network did not hand out", skippedPrivate)
	}
	t.wg.Add(2)
	go t.serve()
	go t.monitor()
	return t, nil
}

// useProbeTargets makes a pinned relay group for each of cfg.ProbeTargets.
func (t *Tunnel) useProbeTargets() {
	if len(t.cfg.ProbeTargets) == 0 {
		return
	}
	for _, hp := range t.cfg.ProbeTargets {
		host, port, err := net.SplitHostPort(hp)
		if err != nil || net.ParseIP(host) != nil {
			continue
		}
		g := t.relayLocked(host)
		g.pinned = true
		g.hosts[g.name] = time.Now()
		g.ports[g.name] = port
	}
	t.log.add("probing fixed hosts: %s", strings.Join(t.cfg.ProbeTargets, ", "))
}

// relayLocked returns host's relay group, making it on first use and
// dropping the least recently used unpinned relay without connections when
// there are too many. Caller holds t.mu.
func (t *Tunnel) relayLocked(host string) *group {
	host = strings.ToLower(host)
	if g := t.relays[host]; g != nil {
		return g
	}
	g := newGroup(host, kindRelay, initialRoute(t.mode))
	g.seen = true
	t.relays[host] = g
	for len(t.relays) > maxRelayGroups {
		var oldest *group
		for _, r := range t.relays {
			if r == g || r.pinned || t.hasConnsLocked(r) {
				continue
			}
			if oldest == nil || r.used.Before(oldest.used) {
				oldest = r
			}
		}
		if oldest == nil {
			break
		}
		delete(t.relays, oldest.name)
	}
	return g
}

func (t *Tunnel) hasConnsLocked(g *group) bool {
	for c := range t.conns {
		if c.group == g {
			return true
		}
	}
	return false
}

// kindOf classifies a CONNECT destination: Telegram's addresses, other IP
// literals, or a relay by host name.
func kindOf(addr string) groupKind {
	host, _, err := net.SplitHostPort(addr)
	switch {
	case err != nil:
		return kindOther
	case isTelegram(host):
		return kindTelegram
	case net.ParseIP(host) != nil:
		return kindOther
	default:
		return kindRelay
	}
}

// groupForLocked returns addr's group, making a relay's on first use.
// Caller holds t.mu.
func (t *Tunnel) groupForLocked(addr string) *group {
	switch kindOf(addr) {
	case kindTelegram:
		return t.telegram
	case kindRelay:
		host, _, _ := net.SplitHostPort(addr)
		return t.relayLocked(host)
	default:
		return t.other
	}
}

// groupsLocked lists every group: Telegram, other, then the relays by name.
// Caller holds t.mu.
func (t *Tunnel) groupsLocked() []*group {
	out := []*group{t.telegram, t.other}
	names := make([]string, 0, len(t.relays))
	for n := range t.relays {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, t.relays[n])
	}
	return out
}

func initialRoute(mode string) Route {
	if mode == ModeAlways {
		return RouteTunnel
	}
	return RouteDirect
}

// learnDNSEgress records which resolver really answers on an intercepting
// network, for the report.
func (t *Tunnel) learnDNSEgress() {
	ctx, cancel := context.WithTimeout(t.ctx, 4*time.Second)
	defer cancel()
	ip, err := dnsWhoami(ctx, "77.88.8.8:53")
	if err != nil {
		t.log.add("DNS interceptor not identified: %v", err)
		return
	}
	t.mu.Lock()
	t.dnsEgress = ip.String()
	t.mu.Unlock()
	t.log.add("DNS queries on this network are answered via %s", ip)
}

// RouteGen counts route changes, so an embedding app can make its clients
// reconnect at once.
func (t *Tunnel) RouteGen() int64 { return t.routeGen.Load() }

// Addr is the SOCKS5 listen address.
func (t *Tunnel) Addr() string { return t.ln.Addr().String() }

// Close stops serving and drops every connection.
func (t *Tunnel) Close() {
	t.cancel()
	t.ln.Close()
	t.mu.Lock()
	var locals []net.Conn
	for c := range t.clients {
		locals = append(locals, c)
	}
	var all []*tracked
	for c := range t.conns {
		all = append(all, c)
	}
	t.mu.Unlock()
	for _, c := range locals {
		c.Close()
	}
	for _, c := range all {
		c.local.Close()
		c.remote.Close()
	}
	if t.client != nil {
		t.client.Close()
	}
	t.wg.Wait()
}

// restrict limits the tunnel to routes of one type ("" = all). It reports
// false when no configured route has that type.
func (t *Tunnel) restrict(kind string) bool {
	ok := false
	switch c := t.client.(type) {
	case nil:
		ok = kind == ""
	case *routePool:
		ok = c.restrict(kind)
	default: // a link without a route list: one soar route
		ok = kind == "" || kind == "soar"
	}
	if ok {
		t.mu.Lock()
		t.only = kind
		t.mu.Unlock()
	}
	return ok
}

// SetMode switches between auto, always and off, or forces the tunnel
// through one route type.
func (t *Tunnel) SetMode(mode string) error {
	kind := ""
	switch mode {
	case ModeAuto, ModeAlways, ModeOff:
	default:
		if !slices.Contains(RouteKinds, mode) {
			return errors.New("unknown mode " + mode)
		}
		kind, mode = mode, ModeAlways
	}
	if !t.restrict(kind) {
		return errors.New("no " + kind + " route configured")
	}
	t.mu.Lock()
	t.mode = mode
	if mode == ModeAlways && t.ways[RouteTunnel].dial == nil {
		mode = ModeAuto
		t.mode = mode
	}
	for _, g := range t.groupsLocked() {
		g.failStreak, g.okStreak = 0, 0
		switch mode {
		case ModeAlways:
			t.setRouteLocked(g, RouteTunnel, "mode always")
		case ModeOff:
			t.setRouteLocked(g, RouteDirect, "mode off")
		case ModeAuto:
			// Start auto from direct; the monitor moves to the tunnel if needed.
			t.setRouteLocked(g, RouteDirect, "mode auto")
		}
	}
	shown := cmp.Or(t.only, t.mode)
	t.mu.Unlock()
	t.log.add("mode set to %s", shown)
	t.kickProbe()
	return nil
}

// Recheck probes at once, without the usual minimum gap between probes:
// the user asked for it ("Check now").
func (t *Tunnel) Recheck() {
	t.urgent.Store(true)
	t.kickProbe()
}

// NetworkChanged asks for an immediate re-check. The streaks are kept: some
// platforms report spurious changes, and a real network switch restarts the
// tunnel anyway.
func (t *Tunnel) NetworkChanged() {
	t.kickProbe()
}

func (t *Tunnel) kickProbe() {
	select {
	case t.kick <- struct{}{}:
	default:
	}
}

// setRouteLocked changes g's route and closes g's connections on other
// routes, so clients reconnect over the new route at once. Caller holds t.mu.
func (t *Tunnel) setRouteLocked(g *group, r Route, reason string) {
	if r == g.route {
		return
	}
	old := g.route
	g.route = r
	g.routeSince = time.Now()
	g.tunnelFails = 0
	t.routeGen.Add(1)
	t.log.add("%s route %s -> %s (%s)", g.name, old, r, reason)
	for c := range t.conns {
		if c.group == g && c.route != r {
			// Close only wakes the pipes; their cleanup takes t.mu after we release it.
			c.local.Close()
			c.remote.Close()
		}
	}
}

func (t *Tunnel) serve() {
	defer t.wg.Done()
	for {
		c, err := t.ln.Accept()
		if err != nil {
			if t.ctx.Err() != nil {
				return
			}
			t.log.add("accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		t.mu.Lock()
		if t.ctx.Err() != nil || len(t.clients) >= maxConnections || t.pending >= maxPendingHandshakes {
			t.mu.Unlock()
			c.Close()
			continue
		}
		if t.clients == nil {
			t.clients = make(map[net.Conn]bool)
		}
		t.clients[c] = true
		t.pending++
		t.wg.Add(1)
		t.mu.Unlock()
		go func() {
			defer t.wg.Done()
			defer func() {
				t.mu.Lock()
				if t.clients[c] {
					t.pending--
				}
				delete(t.clients, c)
				t.mu.Unlock()
			}()
			t.handle(c)
		}()
	}
}

func (t *Tunnel) handle(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(handshakeTimeout))
	addr, err := socksHandshake(c, t.cfg.User, t.cfg.Pass)
	if err != nil {
		t.log.add("socks: %v", err)
		return
	}
	c.SetDeadline(time.Time{})
	t.mu.Lock()
	if t.clients[c] {
		t.clients[c] = false
		t.pending--
	}
	g := t.groupForLocked(addr)
	t.mu.Unlock()
	t.learn(g, addr)

	remote, route, err := t.dial(g, addr)
	c.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	if err != nil {
		if errors.Is(err, errNoIPv6Route) {
			socksReply(c, repNetworkUnreachable)
			return
		}
		t.mu.Lock()
		t.stats[route].Fails++
		if route == RouteTunnel {
			g.tunnelFails++
		}
		t.mu.Unlock()
		t.log.add("%s dial %s failed: %v", route, addr, err)
		socksReply(c, repHostUnreachable)
		return
	}
	defer remote.Close()
	if err := socksReply(c, repSucceeded); err != nil {
		return
	}
	c.SetDeadline(time.Time{})

	tr := &tracked{group: g, route: route, local: c, remote: remote}
	t.mu.Lock()
	if t.ctx.Err() != nil {
		t.mu.Unlock()
		return
	}
	t.conns[tr] = struct{}{}
	t.stats[route].Conns++
	if route == RouteTunnel {
		g.via = connVia(remote)
		if g.via == "" && t.cfg.Zone != "" {
			g.via = "soar"
		}
	}
	t.mu.Unlock()

	start := time.Now()
	var up, down atomic.Int64
	if route != RouteTunnel {
		stall := time.AfterFunc(firstResponseTimeout, func() {
			if up.Load() > 0 && down.Load() == 0 {
				t.kickProbe()
			}
		})
		defer stall.Stop()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pipe(remote, c, &up, &t.upBytes[route]) }()
	go func() { defer wg.Done(); pipe(c, remote, &down, &t.downBytes[route]) }()
	wg.Wait()

	t.mu.Lock()
	delete(t.conns, tr)
	t.mu.Unlock()
	// A direct connection that died young with almost nothing received:
	// check now instead of at the next scheduled probe.
	if route == RouteDirect && time.Since(start) < suspiciousLifetime && down.Load() < suspiciousBytes {
		t.kickProbe()
	}
}

// learn marks g as used and records addr as its probe host: a relay its own
// name, Telegram the addresses it connects to (up to maxTelegramHosts).
func (t *Tunnel) learn(g *group, addr string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	g.used = time.Now()
	switch g.kind {
	case kindRelay:
		g.hosts[g.name] = time.Now()
		g.ports[g.name] = probePort(addr)
	case kindTelegram:
		if !g.seen {
			g.seen = true
			t.kickProbe()
		}
		g.hosts[host] = time.Now()
		g.ports[host] = port
		for len(g.hosts) > maxTelegramHosts {
			oldest := ""
			for h, at := range g.hosts {
				if oldest == "" || at.Before(g.hosts[oldest]) {
					oldest = h
				}
			}
			delete(g.hosts, oldest)
			delete(g.ports, oldest)
		}
	}
}

var errNoIPv6Route = errors.New("no IPv6 route available")

// dial opens addr on g's current route. In auto mode a failed dial falls
// through to the next usable route for this connection and triggers a probe.
// The error of the last route tried is returned for the caller to count.
func (t *Tunnel) dial(g *group, addr string) (net.Conn, Route, error) {
	ipv6 := isIPv6Address(addr)
	localIPv6 := !ipv6 || t.hasIPv6Route()
	usable := func(r Route) bool {
		if !t.usableLocked(g, r) {
			return false
		}
		if !ipv6 {
			return true
		}
		if r == RouteTunnel {
			return t.cfg.TunnelIPv6
		}
		return localIPv6
	}
	t.mu.Lock()
	route, mode := g.route, t.mode
	if ipv6 && !usable(route) {
		if mode != ModeAuto {
			t.mu.Unlock()
			return nil, route, errNoIPv6Route
		}
		for route = RouteDirect; route < numRoutes && !usable(route); route++ {
		}
		if route == numRoutes {
			t.mu.Unlock()
			return nil, RouteDirect, errNoIPv6Route
		}
	}
	t.mu.Unlock()
	for r := route; ; {
		t.mu.Lock()
		w := t.ways[r]
		if !usable(r) {
			w = way{}
		}
		next := r
		for n := r + 1; n < numRoutes; n++ {
			if usable(n) {
				next = n
				break
			}
		}
		t.mu.Unlock()
		if w.dial == nil { // the smart route went away since the route was chosen
			if next == r {
				return nil, r, errors.New(r.String() + " route not available")
			}
			r = next
			continue
		}
		ctx, cancel := context.WithTimeout(t.ctx, w.timeout)
		c, err := w.dial(ctx, "tcp", addr)
		cancel()
		if err == nil {
			return c, r, nil
		}
		if next == r {
			return nil, r, err
		}
		t.kickProbe()
		if mode != ModeAuto {
			return nil, r, err
		}
		t.mu.Lock()
		t.stats[r].Fails++
		t.mu.Unlock()
		t.log.add("%s dial %s failed (%v); trying %s", r, addr, err, next)
		r = next
	}
}

// nextRouteLocked is the next route after r usable for g, or r itself if
// there is none. Caller holds t.mu.
func (t *Tunnel) nextRouteLocked(g *group, r Route) Route {
	for n := r + 1; n < numRoutes; n++ {
		if t.usableLocked(g, n) {
			return n
		}
	}
	return r
}

// usableLocked: r is set up, and allowed for g (MTProto is not TLS, so the
// smart route's strategies don't apply to Telegram). Caller holds t.mu.
func (t *Tunnel) usableLocked(g *group, r Route) bool {
	return t.ways[r].dial != nil && !(g.kind == kindTelegram && r == RouteSmart)
}

func (t *Tunnel) strategyNameLocked() string {
	if t.strategy == nil {
		return ""
	}
	return t.strategy.name
}

// routes lists the set-up routes in order. Caller holds t.mu.
func (t *Tunnel) routes() []string {
	var out []string
	for r := Route(0); r < numRoutes; r++ {
		if t.ways[r].dial != nil {
			out = append(out, r.String())
		}
	}
	return out
}

// pipe copies src to dst, counting bytes into conn and route as they flow,
// then half-closes dst so the peer sees EOF.
func pipe(dst, src net.Conn, conn, route *atomic.Int64) {
	io.Copy(countingWriter{dst, conn, route}, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else {
		dst.Close()
	}
}

type countingWriter struct {
	w           io.Writer
	conn, route *atomic.Int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.conn.Add(int64(n))
	c.route.Add(int64(n))
	return n, err
}

func (t *Tunnel) monitor() {
	defer t.wg.Done()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.kick:
		case <-time.After(t.nextProbeIn()):
		}
		t.mu.Lock()
		mode, since := t.mode, time.Since(t.lastProbe)
		t.mu.Unlock()
		if mode != ModeAuto {
			continue
		}
		if since < minProbeGap && !t.urgent.Swap(false) {
			select {
			case <-t.ctx.Done():
				return
			case <-time.After(minProbeGap - since):
			}
		}
		t.check()
	}
}

// check is one monitor round: probe, then apply the verdict to each group
// that had probe hosts.
func (t *Tunnel) check() {
	res := t.probe()
	t.mu.Lock()
	t.lastProbe = time.Now()
	t.mu.Unlock()
	for g, r := range res {
		if r.Relays > 0 {
			t.applyProbe(g, t.confirmViaTunnel(g, r))
		}
	}
}

// nextProbeIn is the shortest wait any probed group asks for.
func (t *Tunnel) nextProbeIn() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.mode != ModeAuto {
		return time.Hour
	}
	next := time.Hour
	for _, g := range t.groupsLocked() {
		if g.seen {
			next = min(next, g.probeIn())
		}
	}
	return next
}

func (gs *group) probeIn() time.Duration {
	switch {
	case gs.state == "offline":
		return 30 * time.Second
	case gs.route != RouteDirect:
		return 30 * time.Second
	case gs.state == stateRelaysDown:
		return time.Minute // each check costs a handshake through the tunnel
	case gs.failStreak > 0:
		return 15 * time.Second
	case gs.state == "unknown":
		return time.Minute
	default:
		return 5 * time.Minute
	}
}

// targetsLocked lists the host:ports to probe for g, most recent first: a
// relay's own name, or Telegram's learned addresses (up to maxProbedRelays)
// and its data centers. Nothing until g has seen traffic. Caller holds t.mu.
func (t *Tunnel) targetsLocked(g *group) []string {
	gs := g
	if !gs.seen || gs.kind == kindOther {
		return nil
	}
	hosts := make([]string, 0, len(gs.hosts))
	for h := range gs.hosts {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool { return gs.hosts[hosts[i]].After(gs.hosts[hosts[j]]) })
	var out []string
	for _, h := range hosts {
		if len(out) == maxProbedRelays {
			break
		}
		port := gs.ports[h]
		if port == "" {
			port = "443"
		}
		if hp := net.JoinHostPort(h, port); !slices.Contains(telegramDCs, hp) {
			out = append(out, hp)
		}
	}
	if g.kind == kindTelegram {
		out = append(out, telegramDCs...)
	}
	return out
}

// probeHost checks one probe host of g, directly or through the tunnel:
// a full TLS handshake for mail (a TCP connect alone proves little), an
// MTProto exchange for Telegram.
func (t *Tunnel) probeHost(ctx context.Context, g *group, viaTunnel bool, hostport string) error {
	host, _, _ := net.SplitHostPort(hostport)
	switch {
	case g.kind == kindTelegram && viaTunnel:
		return t.probeMTTunnel(ctx, hostport)
	case g.kind == kindTelegram:
		return t.probeMT(ctx, hostport)
	case viaTunnel:
		return t.probeTunnel(ctx, hostport, host)
	default:
		return t.probeTLS(ctx, hostport, host)
	}
}

// probe checks targets, then canaries only if no target answered. Smart
// checks run for failed relays, one at a time since they share the strategy.
func (t *Tunnel) probe() map[*group]probeResult {
	t.mu.Lock()
	groups := t.groupsLocked()
	auto := t.mode == ModeAuto
	t.mu.Unlock()
	res := make(map[*group]probeResult, len(groups))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, g := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := t.probeGroup(g)
			mu.Lock()
			res[g] = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	failed := func(r probeResult) bool { return r.Relays > 0 && r.RelayOK == 0 }
	anyFailed, networkOK := false, false
	for _, r := range res {
		anyFailed = anyFailed || failed(r)
		networkOK = networkOK || r.RelayOK > 0
	}
	canaryChecked, canaryOK := false, false
	if anyFailed && !networkOK {
		canaryChecked = true
		canaryOK = t.probeCanaries()
		networkOK = canaryOK
	}
	for g, r := range res {
		r.NetworkOK = networkOK
		if failed(r) {
			r.CanaryChecked, r.CanaryOK = canaryChecked, canaryOK
		}
		res[g] = r
	}
	if !auto || len(t.strategies) == 0 {
		return res
	}
	for _, g := range groups {
		r := res[g]
		if g.kind != kindRelay || !failed(r) || !r.NetworkOK {
			continue
		}
		t.mu.Lock()
		r.SmartKnown = t.strategy != nil
		t.mu.Unlock()
		r.SmartChecked = true
		r.SmartOK = t.smartCheck(r.hosts)
		if r.SmartOK {
			t.mu.Lock()
			r.Strategy = t.strategyNameLocked()
			t.mu.Unlock()
		}
		res[g] = r
	}
	return res
}
func (t *Tunnel) probeGroup(g *group) probeResult {
	t.mu.Lock()
	hosts := t.targetsLocked(g)
	t.mu.Unlock()
	if g.kind == kindTelegram && !t.hasIPv6Route() {
		hosts, _ = withoutIPv6(hosts)
	}
	res := probeResult{Relays: len(hosts), hosts: hosts}
	var ok atomic.Int32
	var wg sync.WaitGroup
	for _, hp := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(t.ctx, probeTimeout)
			defer cancel()
			if t.probeHost(ctx, g, false, hp) == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	res.RelayOK = int(ok.Load())
	return res
}

// probeCanaries reports whether any canary completes a TLS handshake. They
// go one at a time, the one that answered last first: the next starts when
// the previous fails or has had canaryStagger without an answer, and the
// first answer cancels the rest.
func (t *Tunnel) probeCanaries() bool {
	t.mu.Lock()
	order := append([]string(nil), t.cfg.Canaries...)
	last := t.lastCanary
	t.mu.Unlock()
	if i := slices.Index(order, last); i > 0 {
		order = append([]string{last}, slices.Delete(order, i, i+1)...)
	}
	if len(order) == 0 {
		return false
	}
	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	type answer struct {
		canary string
		ok     bool
	}
	answers := make(chan answer, len(order)) // buffered: late answers never block
	start := func(c string) {
		go func() {
			pctx, pcancel := context.WithTimeout(ctx, probeTimeout)
			defer pcancel()
			host, _, _ := net.SplitHostPort(c)
			answers <- answer{c, t.probeTLS(pctx, c, host) == nil}
		}()
	}
	next, running := 1, 1
	start(order[0])
	for running > 0 {
		var stagger <-chan time.Time
		if next < len(order) {
			stagger = time.After(canaryStagger)
		}
		select {
		case a := <-answers:
			running--
			if a.ok {
				t.mu.Lock()
				t.lastCanary = a.canary
				t.mu.Unlock()
				return true
			}
		case <-stagger:
		case <-ctx.Done():
			return false
		}
		// A failure or a stagger tick: bring in the next canary, if any.
		if next < len(order) {
			start(order[next])
			next++
			running++
		}
	}
	return false
}

// confirmViaTunnel checks before switching, and when nothing answers directly.
// A tunnel answer proves connectivity even when all direct access is blocked.
// With direct connectivity, failure through a live session means relays-down.
func (t *Tunnel) confirmViaTunnel(g *group, res probeResult) probeResult {
	t.mu.Lock()
	gs := g
	check := t.mode == ModeAuto && !res.SmartOK && res.Relays > 0 && res.RelayOK == 0 &&
		(!res.NetworkOK || gs.route == RouteDirect && gs.failStreak+1 >= blockedAfter)
	t.mu.Unlock()
	canProbe := t.probeTunnel != nil
	if g.kind == kindTelegram {
		canProbe = t.probeMTTunnel != nil
	}
	if !check || !canProbe || t.established == nil {
		return res
	}
	hosts := res.hosts
	if !t.cfg.TunnelIPv6 {
		hosts, _ = withoutIPv6(hosts)
	}
	if len(hosts) == 0 {
		return res
	}
	var ok atomic.Bool
	var wg sync.WaitGroup
	for _, hp := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(t.ctx, tunnelProbeTimeout)
			defer cancel()
			if t.probeHost(ctx, g, true, hp) == nil {
				ok.Store(true)
			}
		}()
	}
	wg.Wait()
	if ok.Load() || t.established() {
		res.TunnelChecked, res.TunnelOK = true, ok.Load()
	}
	return res
}

func (t *Tunnel) applyProbe(g *group, res probeResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	gs := g
	gs.lastProbe = time.Now()
	gs.lastResult = res
	healthy := gs.tunnelFails == 0 && (t.client == nil || t.client.Established())
	route, fail, ok, state := decide(gs.route, res, gs.failStreak, gs.okStreak, healthy)
	if g.kind == kindTelegram && route == RouteSmart {
		route = RouteTunnel
	}
	if !t.usableLocked(g, route) { // no tunnel without a link, no smart without a strategy
		route = RouteDirect
	}
	if state != gs.state {
		via := fmt.Sprintf(", network ok=%v", res.NetworkOK)
		if res.CanaryChecked {
			via += fmt.Sprintf(", canary ok=%v", res.CanaryOK)
		}
		if res.SmartChecked {
			via += fmt.Sprintf(", smart ok=%v", res.SmartOK)
		}
		if res.TunnelChecked {
			via += fmt.Sprintf(", via tunnel ok=%v", res.TunnelOK)
		}
		t.log.add("%s state %s -> %s (probe hosts %d/%d ok%s)", g.name, gs.state, state, res.RelayOK, res.Relays, via)
	}
	gs.failStreak, gs.okStreak, gs.state = fail, ok, state
	if t.mode == ModeAuto {
		t.setRouteLocked(g, route, state)
	}
}

// decide is the monitor's state machine, kept pure for tests.
//   - any probe host answering directly means the network is open; leaving
//     the tunnel takes recoveredAfter such probes, or one if it is failing;
//   - every relay failing while the network is reachable means "blocked": the
//     smart route if a strategy works, else the tunnel, unless the relays
//     fail through a live tunnel too: then they are down ("relays-down")
//     and the route stays;
//   - a tunnel answer also proves direct access is blocked;
//   - everything failing means "offline": change nothing.
func decide(route Route, res probeResult, failStreak, okStreak int, tunnelHealthy bool) (Route, int, int, string) {
	switch {
	case res.Relays == 0:
		return route, failStreak, okStreak, "unknown"
	case res.RelayOK > 0:
		okStreak++
		// Leaving smart or a working tunnel waits for recoveredAfter good
		// probes (no flapping); a failing tunnel is left after the first.
		if route != RouteDirect && (okStreak >= recoveredAfter || route == RouteTunnel && !tunnelHealthy) {
			route = RouteDirect
		}
		return route, 0, okStreak, "direct"
	case res.NetworkOK || res.TunnelOK:
		failStreak++
		switch {
		case res.SmartOK && (route != RouteDirect || failStreak >= blockedAfter || res.SmartKnown):
			route = RouteSmart
		case route == RouteDirect && failStreak >= blockedAfter:
			if res.TunnelChecked && !res.TunnelOK {
				return route, failStreak, 0, stateRelaysDown
			}
			route = RouteTunnel
		case route == RouteSmart: // its strategy stopped working
			route = RouteTunnel
		}
		return route, failStreak, 0, "blocked"
	default:
		return route, failStreak, 0, "offline"
	}
}

// probePort is the port to TLS-probe a host on: the one the client connects to
// when it starts with TLS (IMAPS 993, SMTPS 465, ALPN 443). Plain-start ports
// (143/587 use STARTTLS) can't be TLS-probed directly; chatmail relays serve
// TLS on 443 for those.
func probePort(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		switch port {
		case "993", "465", "443":
			return port
		}
	}
	return "443"
}

// stateRelaysDown: the relays fail directly and through the tunnel.
const stateRelaysDown = "relays-down"

// probeTLSVia completes a TLS handshake with hostport over a connection from dial.
func probeTLSVia(ctx context.Context, dial dialFunc, hostport, sni string) error {
	c, err := dial(ctx, "tcp", hostport)
	if err != nil {
		return err
	}
	defer c.Close()
	return tls.Client(c, &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12, RootCAs: probeRoots}).HandshakeContext(ctx)
}

// probeRoots verifies probe handshakes; nil means the system roots. Tests set it.
var probeRoots *x509.CertPool

func probeTLS(ctx context.Context, hostport, sni string) error {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: probeTimeout},
		Config:    &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12, RootCAs: probeRoots},
	}
	c, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return err
	}
	return c.Close()
}

// hasIPv6Route reports whether this device can reach the IPv6 internet:
// connecting a UDP socket only asks the kernel for a route (no packet is
// sent), and fails at once without one.
func hasIPv6Route() bool {
	c, err := net.Dial("udp6", "[2001:4860:4860::8888]:53")
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func (t *Tunnel) hasIPv6Route() bool {
	if t.ipv6Route != nil {
		return t.ipv6Route()
	}
	return hasIPv6Route()
}

func isIPv6Address(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() == nil
}

// withoutForeignPrivate drops resolvers on private addresses (RFC 1918,
// CGNAT 100.64/10, IPv6 ULA) unless the current network handed them out.
func withoutForeignPrivate(resolvers, network []string) ([]string, int) {
	own := map[string]bool{}
	for _, r := range network {
		own[r] = true
	}
	out := make([]string, 0, len(resolvers))
	for _, r := range resolvers {
		if !own[r] && isPrivateResolver(r) {
			continue
		}
		out = append(out, r)
	}
	return out, len(resolvers) - len(out)
}

var cgnat = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isPrivateResolver(r string) bool {
	host, _, err := net.SplitHostPort(r)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || cgnat.Contains(ip))
}

// ipv4First moves IPv6 resolvers after the IPv4 ones, keeping each group's
// order: soar sends its first hellos down the first paths.
func ipv4First(resolvers []string) []string {
	var v4, v6 []string
	for _, r := range resolvers {
		host, _, err := net.SplitHostPort(r)
		if ip := net.ParseIP(host); err == nil && ip != nil && ip.To4() == nil {
			v6 = append(v6, r)
		} else {
			v4 = append(v4, r)
		}
	}
	return append(v4, v6...)
}

// withoutIPv6 drops IPv6 addresses, returning how many it dropped.
func withoutIPv6(resolvers []string) ([]string, int) {
	out := make([]string, 0, len(resolvers))
	for _, r := range resolvers {
		if isIPv6Address(r) {
			continue
		}
		out = append(out, r)
	}
	return out, len(resolvers) - len(out)
}

// isDoT reports DNS-over-TLS resolvers (port 853): they speak TCP+TLS only.
func isDoT(resolver string) bool {
	_, port, err := net.SplitHostPort(resolver)
	return err == nil && port == "853"
}

// dialResolver opens soar's TCP paths: TLS for port 853 (DNS-over-TLS uses
// the same 2-byte length framing as soar's TCP/53 paths), plain TCP otherwise.
func dialResolver(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	if port != "853" {
		return d.DialContext(ctx, network, addr)
	}
	td := &tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	return td.DialContext(ctx, network, addr)
}

// Status is the diagnostics snapshot shown in the app and sent in reports.
type Status struct {
	RouteCount int                   `json:"route_count"`
	Zone       string                `json:"zone"`
	TunnelIPv6 bool                  `json:"tunnel_ipv6"`
	Mode       string                `json:"mode"`
	Route      string                `json:"route"`
	Via        string                `json:"via,omitempty"`      // route type under "tunnel": iroh, soar
	Strategy   string                `json:"strategy,omitempty"` // the smart route's, when it works
	Routes     []string              `json:"routes"`             // set-up routes, in fallback order
	RouteSince time.Time             `json:"route_since"`
	State      string                `json:"state"`
	Resolvers  int                   `json:"resolvers"`
	Network    []string              `json:"network_resolvers"`
	Operator   string                `json:"operator"`
	Relays     []string              `json:"relays"`
	LastProbe  time.Time             `json:"last_probe"`
	LastResult probeResult           `json:"last_result"`
	Active     map[string]int        `json:"active"`
	Stats      map[string]routeStats `json:"stats"`
	Soar       string                `json:"soar"`
	// Tunnels has one row per configured tunnel route, in priority order.
	Tunnels []RouteState `json:"tunnels"`
	// Session is whether the soar session with the server is up; Paths is
	// one row per resolver path (which resolvers carry the tunnel here).
	Session bool `json:"session"`
	// DNSIntercepted: this network redirects port 53; DNSEgress: who
	// actually resolves for it (whoami.akamai.net), when learned.
	DNSIntercepted bool            `json:"dns_intercepted"`
	DNSEgress      string          `json:"dns_egress,omitempty"`
	Paths          []soar.PathStat `json:"paths"`
	Events         []Event         `json:"events"`
	// Groups has each group's verdict, by relay host name or "telegram". The
	// top-level route, state and last probe are the most recently used
	// relay's, or Telegram's when there are no relays.
	Groups map[string]GroupStatus `json:"groups"`
}

// GroupStatus is one group's part of Status.
type GroupStatus struct {
	Kind       string      `json:"kind"` // relay or telegram
	Route      string      `json:"route"`
	Via        string      `json:"via,omitempty"`
	RouteSince time.Time   `json:"route_since"`
	State      string      `json:"state"`
	LastProbe  time.Time   `json:"last_probe"`
	LastResult probeResult `json:"last_result"`
	Targets    []string    `json:"targets"` // what the next probe checks
}

// Status returns a snapshot with the last n events.
func (t *Tunnel) Status(n int) Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Follow the most recently used relay, or Telegram when used alone.
	mail := t.other
	for _, g := range t.relays {
		if mail == t.other || g.used.After(mail.used) {
			mail = g
		}
	}
	if mail == t.other && t.telegram.seen {
		mail = t.telegram
	}
	s := Status{
		Zone:           t.cfg.Zone,
		RouteCount:     len(t.cfg.Routes),
		TunnelIPv6:     t.cfg.HasTunnel() && t.cfg.TunnelIPv6,
		Mode:           cmp.Or(t.only, t.mode),
		Route:          mail.route.String(),
		Via:            mail.viaLocked(),
		RouteSince:     mail.routeSince.UTC(),
		State:          mail.state,
		Resolvers:      len(t.resolvers),
		Network:        t.cfg.SystemResolvers,
		DNSIntercepted: t.intercepted,
		DNSEgress:      t.dnsEgress,
		Operator:       t.cfg.Operator,
		LastProbe:      mail.lastProbe.UTC(),
		LastResult:     mail.lastResult,
		Routes:         t.routes(),
		Strategy:       t.strategyNameLocked(),
		Active:         map[string]int{},
		Stats:          map[string]routeStats{},
		Soar:           SoarVersion(),
		Groups:         map[string]GroupStatus{},
	}
	for _, gs := range t.groupsLocked() {
		if gs.kind == kindOther {
			continue
		}
		s.Groups[gs.name] = GroupStatus{
			Kind:       gs.kind.String(),
			Route:      gs.route.String(),
			Via:        gs.viaLocked(),
			RouteSince: gs.routeSince.UTC(),
			State:      gs.state,
			LastProbe:  gs.lastProbe.UTC(),
			LastResult: gs.lastResult,
			Targets:    append([]string{}, t.targetsLocked(gs)...),
		}
	}
	for r := Route(0); r < numRoutes; r++ {
		s.Active[r.String()] = 0
		s.Stats[r.String()] = t.routeStatsLocked(r)
	}
	for name := range t.relays {
		s.Relays = append(s.Relays, name)
	}
	sort.Strings(s.Relays)
	for c := range t.conns {
		s.Active[c.route.String()]++
	}
	s.Events = t.log.last(n)
	s.Tunnels = []RouteState{}
	if p, ok := t.client.(*routePool); ok {
		s.Tunnels = p.states()
	} else if t.client != nil {
		s.Tunnels = append(s.Tunnels, RouteState{ID: "dns", Type: "soar", State: routeIdle})
		if t.client.Established() {
			s.Tunnels[0].State = routeUp
		}
	}
	if t.client != nil {
		s.Session = t.client.Established()
		s.Paths = t.client.PathStats()
		for i, p := range s.Paths {
			// DoT paths are soar TCP paths to port 853 (dialResolver wraps TLS).
			if _, port, err := net.SplitHostPort(p.Resolver); err == nil && port == "853" && p.Transport == "tcp" {
				s.Paths[i].Transport = "dot"
			}
		}
	}
	return s
}

func (t *Tunnel) routeStatsLocked(r Route) routeStats {
	s := t.stats[r]
	s.Up, s.Down = t.upBytes[r].Load(), t.downBytes[r].Load()
	return s
}

// SoarVersion is the soar module version this binary was built with.
func SoarVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/getlantern/soar" {
				if d.Replace != nil {
					return d.Replace.Path + "@" + d.Replace.Version
				}
				return d.Version
			}
		}
	}
	return "unknown"
}

// RandomCredentials returns a fresh SOCKS5 username/password pair.
func RandomCredentials() (string, string) {
	b := make([]byte, 16)
	rand.Read(b)
	return "passage", hex.EncodeToString(b)
}

// logHandler forwards soar's warnings and errors into the event log.
type logHandler struct {
	log   *eventLog
	attrs []slog.Attr
}

func (h *logHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

func (h *logHandler) Handle(_ context.Context, r slog.Record) error {
	msg := "soar: " + r.Message
	r.Attrs(func(a slog.Attr) bool {
		msg += " " + a.String()
		return true
	})
	for _, a := range h.attrs {
		msg += " " + a.String()
	}
	h.log.add("%s", msg)
	return nil
}

func (h *logHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &logHandler{log: h.log, attrs: append(append([]slog.Attr(nil), h.attrs...), as...)}
}

func (h *logHandler) WithGroup(string) slog.Handler { return h }
