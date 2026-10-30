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

// Group is a set of destinations that share one route decision.
type Group int

const (
	GroupMail     Group = iota // everything not in another group
	GroupTelegram              // IP literals in Telegram's ranges
	numGroups
)

var groupNames = [numGroups]string{"mail", "telegram"}

func (g Group) String() string { return groupNames[g] }

// groupOf classifies a CONNECT destination.
func groupOf(addr string) Group {
	if host, _, err := net.SplitHostPort(addr); err == nil && isTelegram(host) {
		return GroupTelegram
	}
	return GroupMail
}

// group is one group's route, the monitor's verdict on it, and its probe hosts.
type group struct {
	route       Route
	routeSince  time.Time
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

func newGroups() [numGroups]group {
	var gs [numGroups]group
	for i := range gs {
		gs[i] = group{hosts: map[string]time.Time{}, ports: map[string]string{}, state: "unknown"}
	}
	gs[GroupMail].seen = true // its probe hosts come from config or traffic
	return gs
}

// maxTelegramHosts bounds the destinations remembered for the Telegram group.
const maxTelegramHosts = 32

// way is how a route dials; a nil dial means the route isn't set up here.
type way struct {
	dial    dialFunc
	timeout time.Duration
}

// soar tuning: full-length query names and a wider per-path window.
// Config.SafeQName overrides the starting length.
const (
	soarSafeQName  = 253
	soarPathWindow = 64
)

// Timeouts and the monitor's hysteresis.
const (
	handshakeTimeout      = 30 * time.Second
	directDialTimeout     = 8 * time.Second
	tunnelDialTimeout     = 60 * time.Second
	probeTimeout          = 6 * time.Second
	tunnelProbeTimeout    = 30 * time.Second // a cold soar session needs its hellos first
	minProbeGap           = 10 * time.Second
	blockedAfter          = 2 // consecutive "relays fail, canary ok" probes before switching to the tunnel
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
	client    *soar.Client
	log       *eventLog
	ctx       context.Context
	cancel    context.CancelFunc
	kick      chan struct{}
	wg        sync.WaitGroup

	// Swappable for tests. ways[RouteSmart] is set while a strategy works;
	// guarded by mu.
	ways       [numRoutes]way
	strategies []strategy
	probeTLS   func(ctx context.Context, hostport, sni string) error
	// probeTunnel is probeTLS through the tunnel. established reports a live
	// soar session: only then does a relay failing through the tunnel prove
	// the relay is down (see confirmViaTunnel).
	probeTunnel func(ctx context.Context, hostport, sni string) error
	established func() bool
	// The Telegram group's probes (telegram.go), directly and through the tunnel.
	probeMT       func(ctx context.Context, hostport string) error
	probeMTTunnel func(ctx context.Context, hostport string) error

	mu          sync.Mutex
	mode        string
	groups      [numGroups]group
	routeGen    atomic.Int64 // bumped on every route change, for the shell to nudge the core
	intercepted bool         // this network redirects port-53 traffic (see intercept.go)
	dnsEgress   string       // who actually resolves for the interceptor, if learned
	conns       map[*tracked]struct{}
	fixedProbes bool      // cfg.ProbeTargets set: the mail group's hosts are fixed and never learn
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
	group  Group
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
	Relays   int  `json:"relays"`
	RelayOK  int  `json:"relay_ok"`
	CanaryOK bool `json:"canary_ok"`
	// CanaryChecked: the canaries were probed. They are skipped when a relay
	// answers directly (the network is open whatever they say) or when
	// there is no relay to probe yet.
	CanaryChecked bool `json:"canary_checked,omitempty"`
	// SmartChecked: the probe hosts were tried on the smart route (they
	// failed directly while a canary answered); SmartOK: a strategy works.
	SmartChecked bool `json:"smart_checked,omitempty"`
	SmartOK      bool `json:"smart_ok,omitempty"`
	// SmartKnown: the strategy had worked on this network before this probe,
	// so one blocked probe is enough to switch to it.
	SmartKnown bool   `json:"smart_known,omitempty"`
	Strategy   string `json:"strategy,omitempty"`
	// Set by confirmViaTunnel before a switch to the tunnel: the same relays
	// probed through a live tunnel session.
	TunnelChecked bool `json:"tunnel_checked,omitempty"`
	TunnelOK      bool `json:"tunnel_ok,omitempty"`

	hosts []string // the host:ports probed, most recent first
}

// Start validates cfg, creates the soar client and starts serving SOCKS5.
func Start(cfg Config) (*Tunnel, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	t := &Tunnel{
		cfg:    cfg,
		log:    newEventLog(500),
		kick:   make(chan struct{}, 1),
		mode:   cfg.Mode,
		conns:  map[*tracked]struct{}{},
		groups: newGroups(),
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	var skipped6, skippedPrivate int
	if cfg.HasTunnel() {
		key, _ := base64.StdEncoding.DecodeString(cfg.Key)
		var resolvers []string
		resolvers, skippedPrivate = withoutForeignPrivate(cfg.Effective(), cfg.SystemResolvers)
		resolvers = ipv4First(resolvers)
		// On a network that redirects port 53, lead with the DoT paths.
		t.intercepted = detectInterception(context.Background(), interceptProbeTimeout)
		if t.intercepted {
			resolvers = dotFirst(resolvers)
		}
		if !hasIPv6Route() {
			// Without an IPv6 route these paths can only fail. A network change
			// restarts the tunnel and re-checks.
			resolvers, skipped6 = withoutIPv6(resolvers)
		}
		client, err := soar.NewClient(soar.Config{
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
		if err != nil {
			return nil, err
		}
		t.client = client
		t.resolvers = resolvers
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
	t.ln = ln
	t.useProbeTargets()
	for g := range numGroups {
		t.setRouteLocked(g, initialRoute(cfg.Mode), "start")
	}
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

// useProbeTargets puts cfg.ProbeTargets in place of the mail group's learned hosts.
func (t *Tunnel) useProbeTargets() {
	if len(t.cfg.ProbeTargets) == 0 {
		return
	}
	t.fixedProbes = true
	now := time.Now()
	mail := &t.groups[GroupMail]
	for _, hp := range t.cfg.ProbeTargets {
		host, port, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		mail.hosts[host] = now
		mail.ports[host] = port
	}
	t.log.add("probing fixed hosts: %s", strings.Join(t.cfg.ProbeTargets, ", "))
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
	var all []*tracked
	for c := range t.conns {
		all = append(all, c)
	}
	t.mu.Unlock()
	for _, c := range all {
		c.local.Close()
		c.remote.Close()
	}
	if t.client != nil {
		t.client.Close()
	}
	t.wg.Wait()
}

// SetMode switches between auto, always and off.
func (t *Tunnel) SetMode(mode string) error {
	switch mode {
	case ModeAuto, ModeAlways, ModeOff:
	default:
		return errors.New("unknown mode " + mode)
	}
	t.mu.Lock()
	t.mode = mode
	if mode == ModeAlways && t.ways[RouteTunnel].dial == nil {
		mode = ModeAuto
		t.mode = mode
	}
	for g := range numGroups {
		t.groups[g].failStreak, t.groups[g].okStreak = 0, 0
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
	t.mu.Unlock()
	t.log.add("mode set to %s", mode)
	t.kickProbe()
	return nil
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
func (t *Tunnel) setRouteLocked(g Group, r Route, reason string) {
	gs := &t.groups[g]
	if r == gs.route && !gs.routeSince.IsZero() {
		return
	}
	old := gs.route
	gs.route = r
	gs.routeSince = time.Now()
	gs.tunnelFails = 0
	if reason != "start" {
		t.routeGen.Add(1)
		t.log.add("%s route %s -> %s (%s)", g, old, r, reason)
	}
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
		go t.handle(c)
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
	g := groupOf(addr)
	t.learn(g, addr)

	remote, route, err := t.dial(g, addr)
	if err != nil {
		t.mu.Lock()
		t.stats[route].Fails++
		if route == RouteTunnel {
			t.groups[g].tunnelFails++
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
	t.conns[tr] = struct{}{}
	t.stats[route].Conns++
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

// learn marks g as used and records addr as one of its probe hosts: mail
// learns host names (unless fixed), Telegram the addresses it connects to.
func (t *Tunnel) learn(g Group, addr string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	gs := &t.groups[g]
	if !gs.seen {
		gs.seen = true
		t.kickProbe()
	}
	switch {
	case g == GroupTelegram:
		gs.hosts[host] = time.Now()
		gs.ports[host] = port
		for len(gs.hosts) > maxTelegramHosts {
			oldest := ""
			for h, at := range gs.hosts {
				if oldest == "" || at.Before(gs.hosts[oldest]) {
					oldest = h
				}
			}
			delete(gs.hosts, oldest)
			delete(gs.ports, oldest)
		}
	case net.ParseIP(host) == nil && !t.fixedProbes:
		gs.hosts[host] = time.Now()
		gs.ports[host] = probePort(addr)
	}
}

// dial opens addr on g's current route. In auto mode a failed dial falls
// through to the next usable route for this connection and triggers a probe.
// The error of the last route tried is returned for the caller to count.
func (t *Tunnel) dial(g Group, addr string) (net.Conn, Route, error) {
	t.mu.Lock()
	route, mode := t.groups[g].route, t.mode
	t.mu.Unlock()
	for r := route; ; {
		t.mu.Lock()
		w := t.ways[r]
		if !t.usableLocked(g, r) {
			w = way{}
		}
		next := t.nextRouteLocked(g, r)
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
func (t *Tunnel) nextRouteLocked(g Group, r Route) Route {
	for n := r + 1; n < numRoutes; n++ {
		if t.usableLocked(g, n) {
			return n
		}
	}
	return r
}

// usableLocked: r is set up, and allowed for g (MTProto is not TLS, so the
// smart route's strategies don't apply to Telegram). Caller holds t.mu.
func (t *Tunnel) usableLocked(g Group, r Route) bool {
	return t.ways[r].dial != nil && !(g == GroupTelegram && r == RouteSmart)
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
		if since < minProbeGap {
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
	for g := range numGroups {
		if res[g].Relays > 0 {
			t.applyProbe(g, t.confirmViaTunnel(g, res[g]))
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
	for g := range numGroups {
		if gs := &t.groups[g]; gs.seen {
			next = min(next, gs.probeIn())
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

// targetsLocked lists the host:ports to probe for g, most recent first:
// up to maxProbedRelays learned or fixed hosts, and for Telegram its data
// centers too. Nothing until g has seen traffic. Caller holds t.mu.
func (t *Tunnel) targetsLocked(g Group) []string {
	gs := &t.groups[g]
	if !gs.seen {
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
	if g == GroupTelegram {
		out = append(out, telegramDCs...)
	}
	return out
}

// probeHost checks one probe host of g, directly or through the tunnel:
// a full TLS handshake for mail (a TCP connect alone proves little), an
// MTProto exchange for Telegram.
func (t *Tunnel) probeHost(ctx context.Context, g Group, viaTunnel bool, hostport string) error {
	host, _, _ := net.SplitHostPort(hostport)
	switch {
	case g == GroupTelegram && viaTunnel:
		return t.probeMTTunnel(ctx, hostport)
	case g == GroupTelegram:
		return t.probeMT(ctx, hostport)
	case viaTunnel:
		return t.probeTunnel(ctx, hostport, host)
	default:
		return t.probeTLS(ctx, hostport, host)
	}
}

// probe checks each group's probe hosts, then the canaries (they are global)
// once if some group's hosts all failed, then the smart route for mail.
func (t *Tunnel) probe() [numGroups]probeResult {
	var res [numGroups]probeResult
	var wg sync.WaitGroup
	for g := range numGroups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[g] = t.probeGroup(g)
		}()
	}
	wg.Wait()
	failed := func(r probeResult) bool { return r.Relays > 0 && r.RelayOK == 0 }
	if slices.ContainsFunc(res[:], failed) {
		canary := t.probeCanaries()
		for g := range res {
			if failed(res[g]) {
				res[g].CanaryChecked, res[g].CanaryOK = true, canary
			}
		}
	}
	mail := &res[GroupMail]
	t.mu.Lock()
	auto := t.mode == ModeAuto
	t.mu.Unlock()
	if mail.CanaryOK && auto && len(t.strategies) > 0 {
		t.mu.Lock()
		mail.SmartKnown = t.strategy != nil
		t.mu.Unlock()
		mail.SmartChecked = true
		mail.SmartOK = t.smartCheck(mail.hosts)
		if mail.SmartOK {
			t.mu.Lock()
			mail.Strategy = t.strategyNameLocked()
			t.mu.Unlock()
		}
	}
	return res
}

func (t *Tunnel) probeGroup(g Group) probeResult {
	t.mu.Lock()
	hosts := t.targetsLocked(g)
	t.mu.Unlock()
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

// confirmViaTunnel runs when the next "blocked" probe would switch g to the
// tunnel. If a live tunnel session can't reach the probe hosts either, they
// are down rather than blocked, and the route stays. Without a live session
// a failure proves nothing, and the switch goes ahead.
func (t *Tunnel) confirmViaTunnel(g Group, res probeResult) probeResult {
	t.mu.Lock()
	gs := &t.groups[g]
	switching := t.mode == ModeAuto && gs.route == RouteDirect && !res.SmartOK &&
		res.Relays > 0 && res.RelayOK == 0 && res.CanaryOK && gs.failStreak+1 >= blockedAfter
	t.mu.Unlock()
	canProbe := t.probeTunnel != nil
	if g == GroupTelegram {
		canProbe = t.probeMTTunnel != nil
	}
	if !switching || !canProbe || t.established == nil {
		return res
	}
	var ok atomic.Bool
	var wg sync.WaitGroup
	for _, hp := range res.hosts {
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

func (t *Tunnel) applyProbe(g Group, res probeResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	gs := &t.groups[g]
	gs.lastProbe = time.Now()
	gs.lastResult = res
	healthy := gs.tunnelFails == 0 && (t.client == nil || t.client.Established())
	route, fail, ok, state := decide(gs.route, res, gs.failStreak, gs.okStreak, healthy)
	if g == GroupTelegram && route == RouteSmart {
		route = RouteTunnel
	}
	if !t.usableLocked(g, route) { // no tunnel without a link, no smart without a strategy
		route = RouteDirect
	}
	if state != gs.state {
		via := ""
		if res.CanaryChecked {
			via = fmt.Sprintf(", canary ok=%v", res.CanaryOK)
		}
		if res.SmartChecked {
			via += fmt.Sprintf(", smart ok=%v", res.SmartOK)
		}
		if res.TunnelChecked {
			via += fmt.Sprintf(", via tunnel ok=%v", res.TunnelOK)
		}
		t.log.add("%s state %s -> %s (probe hosts %d/%d ok%s)", g, gs.state, state, res.RelayOK, res.Relays, via)
	}
	gs.failStreak, gs.okStreak, gs.state = fail, ok, state
	if t.mode == ModeAuto {
		t.setRouteLocked(g, route, state)
	}
}

// decide is the monitor's state machine, kept pure for tests.
//   - any probe host answering directly means the network is open; leaving
//     the tunnel takes recoveredAfter such probes, or one if it is failing;
//   - every relay failing while the canary answers means "blocked": the
//     smart route if a strategy works, else the tunnel, unless the relays
//     fail through a live tunnel too: then they are down ("relays-down")
//     and the route stays;
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
	case res.CanaryOK:
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

// withoutIPv6 drops IPv6 resolvers, returning how many it dropped.
func withoutIPv6(resolvers []string) ([]string, int) {
	out := make([]string, 0, len(resolvers))
	for _, r := range resolvers {
		if host, _, err := net.SplitHostPort(r); err == nil {
			if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
				continue
			}
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
	Zone       string                `json:"zone"`
	Mode       string                `json:"mode"`
	Route      string                `json:"route"`
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
	// Session is whether the soar session with the server is up; Paths is
	// one row per resolver path (which resolvers carry the tunnel here).
	Session bool `json:"session"`
	// DNSIntercepted: this network redirects port 53; DNSEgress: who
	// actually resolves for it (whoami.akamai.net), when learned.
	DNSIntercepted bool            `json:"dns_intercepted"`
	DNSEgress      string          `json:"dns_egress,omitempty"`
	Paths          []soar.PathStat `json:"paths"`
	Events         []Event         `json:"events"`
	// Groups has each group's verdict; the top-level route, state, relays
	// and last probe are the mail group's.
	Groups map[string]GroupStatus `json:"groups"`
}

// GroupStatus is one group's part of Status.
type GroupStatus struct {
	Route      string      `json:"route"`
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
	mail := &t.groups[GroupMail]
	s := Status{
		Zone:           t.cfg.Zone,
		Mode:           t.mode,
		Route:          mail.route.String(),
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
	for g := range numGroups {
		gs := &t.groups[g]
		s.Groups[g.String()] = GroupStatus{
			Route:      gs.route.String(),
			RouteSince: gs.routeSince.UTC(),
			State:      gs.state,
			LastProbe:  gs.lastProbe.UTC(),
			LastResult: gs.lastResult,
			Targets:    append([]string{}, t.targetsLocked(g)...),
		}
	}
	for r := Route(0); r < numRoutes; r++ {
		s.Active[r.String()] = 0
		s.Stats[r.String()] = t.routeStatsLocked(r)
	}
	for h := range mail.hosts {
		s.Relays = append(s.Relays, h)
	}
	sort.Strings(s.Relays)
	for c := range t.conns {
		s.Active[c.route.String()]++
	}
	s.Events = t.log.last(n)
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
