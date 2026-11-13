package tunnel

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/soar"
	links "github.com/mrgluek/passage-link/link"
)

// A route that failed goes behind the others for routeCooldown, so new
// connections don't each wait on it before the backup.
const (
	routeCooldown       = 30 * time.Second
	routeAttemptTimeout = 8 * time.Second
)

// routeStagger is how long a route gets before the next one starts in
// parallel; the first connection wins and the others are dropped.
var routeStagger = 2 * time.Second

// A destination a route refused is not asked of that route again for
// refusedFor: apps retry at once, and each ask costs the exit work. One the
// exit could not reach is skipped only for unreachableFor.
const (
	refusedFor     = 30 * time.Second
	unreachableFor = 5 * time.Second
)

type routePool struct {
	log          *eventLog
	onFailure    func()
	clients      []soarClient
	ipv6         []bool
	kinds        []string // route type per client, reported as the connection's via
	uses         []string
	exits        []string
	ids          []string
	servers      []routeServer
	established  atomic.Bool
	mu           sync.Mutex
	failedUntil  map[int]time.Time
	cfg          Config
	memoryConfig func() Config
	network      string
	stalls       map[string]map[int]*hold
	refused      map[string]time.Time // "route index|address" -> until
	only         string               // a route type or route:<id>; empty tries all
	now          func() time.Time     // for tests; nil = time.Now
}

func newRoutePool(cfg Config) (soarClient, error) {
	p := &routePool{network: networkKey(cfg), cfg: cfg}
	routes := links.Sorted(cfg.Routes)
	if cfg.My != "" {
		// Before link routes of the same priority.
		at := len(routes)
		for i, r := range routes {
			if r.Priority >= myPriority {
				at = i
				break
			}
		}
		routes = slices.Insert(routes, at, links.Route{ID: "my", Type: "my", Priority: myPriority, IPv6: true})
	}
	for _, r := range routes {
		var client soarClient
		var server routeServer
		switch r.Type {
		case "my":
			c, err := newMyClient(cfg.My)
			if err != nil {
				p.Close()
				return nil, err
			}
			client = c
			server.addr, _, _ = parseMy(cfg.My)
		case "soar":
			params, err := links.Params[links.SoarParams](r)
			if err != nil {
				p.Close()
				return nil, err
			}
			c := cfg
			c.Resolvers = params.Resolvers
			resolvers, _ := withoutForeignPrivate(c.Effective(), cfg.SystemResolvers)
			resolvers = ipv4First(resolvers)
			if !hasIPv6Route() {
				resolvers, _ = withoutIPv6(resolvers)
			}
			client = newLazySoar(soar.Config{Zone: params.Zone, ServerKey: ed25519.PublicKey(params.Key), Resolvers: resolvers, TCP: "auto", SafeQName: cmp.Or(cfg.SafeQName, soarSafeQName), MaxPathWindow: soarPathWindow, DialTCP: dialResolver, StreamOnly: isDoT, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		case "turn":
			params, err := links.Params[links.TURNParams](r)
			if err != nil {
				p.Close()
				return nil, err
			}
			c, err := NewTURNClient(params)
			if err != nil {
				p.Close()
				return nil, err
			}
			client = c
			server = turnServer(params, c)
		case "http":
			params, err := links.Params[links.HTTPParams](r)
			if err != nil {
				p.Close()
				return nil, err
			}
			c, err := NewHTTPClient(params)
			if err != nil {
				p.Close()
				return nil, err
			}
			client = c
			server = httpServer(params, c)
		case "wss":
			if supported, err := links.ValidateRoute(r); err != nil || !supported {
				p.Close()
				if err == nil {
					err = errors.New("unsupported WSS wire")
				}
				return nil, err
			}
			params, err := links.Params[links.WSSParams](r)
			if err != nil {
				p.Close()
				return nil, err
			}
			c, err := NewWSSClient(params)
			if err != nil {
				p.Close()
				return nil, err
			}
			client = c
			server = wssServer(params, c)
		case "iroh":
			client = &irohClient{route: r, binary: cfg.IrohBinary, dir: cfg.PrivateDir, dns: cfg.SystemResolvers,
				now: p.clock, onRestart: func(now time.Time) {
					if p.log != nil {
						p.log.addAt(now, "%s", historyLine(historyTransition{prefix: "restarted", unit: "iroh helper", reason: "no answer"}))
					}
				}}
			if params, err := links.Params[links.IrohParams](r); err == nil {
				server = urlServer(params.Relay)
			}
		default:
			continue
		}
		p.clients = append(p.clients, client)
		p.ipv6 = append(p.ipv6, r.IPv6)
		p.kinds = append(p.kinds, r.Type)
		p.ids = append(p.ids, r.ID)
		p.uses = append(p.uses, r.Use)
		p.exits = append(p.exits, r.Exit)
		p.servers = append(p.servers, server)
	}
	if len(p.clients) == 0 {
		return nil, errors.New("no supported routes")
	}
	p.restoreHolds()
	return p, nil
}
func (p *routePool) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return p.dialContext(ctx, network, address, false)
}

func (p *routePool) probeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return p.dialContext(ctx, network, address, true)
}

func (p *routePool) dialContext(ctx context.Context, network, address string, background bool) (net.Conn, error) {
	p.tick(p.clock())
	host, _, _ := net.SplitHostPort(address)
	order := p.order(net.ParseIP(host))
	if background {
		order = slices.DeleteFunc(order, p.reserveRoute)
	}
	available := slices.Clone(order)
	asked := p.withoutRefused(order, address)
	if len(order) > 0 && len(asked) == 0 {
		return nil, errDestinationRefused
	}
	order = asked
	refusals := 0
	race, stop := context.WithCancel(ctx)
	defer stop()
	type result struct {
		i    int
		conn net.Conn
		err  error
	}
	results := make(chan result, len(order))
	started, pending, reservePending := 0, 0, 0
	canStart := func() bool {
		if started >= len(order) || reservePending > 0 {
			return false
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		now := p.clock()
		p.tickLocked(now)
		reserveAllowed := pending == 0
		for _, i := range available {
			if !p.reserveRoute(i) && !now.Before(p.failedUntil[i]) && !p.stalledLocked(p.network, i) {
				reserveAllowed = false
				break
			}
		}
		for n := started; n < len(order); n++ {
			i := order[n]
			if p.reserveRoute(i) && !reserveAllowed {
				continue
			}
			if pending > 0 && p.stalledLocked(p.network, i) {
				return false
			}
			copy(order[started+1:n+1], order[started:n])
			order[started] = i
			return true
		}
		return false
	}
	startNext := func() {
		n, i := started, order[started]
		started++
		pending++
		if p.reserveRoute(i) {
			reservePending++
		}
		child, cancel := race, context.CancelFunc(func() {})
		if n < len(order)-1 {
			child, cancel = context.WithTimeout(race, routeAttemptTimeout)
		}
		go func() {
			conn, err := p.clients[i].DialContext(child, network, address)
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errDestinationRefused) {
				p.mark(i, true)
			}
			cancel()
			results <- result{i, conn, err}
		}()
	}
	// drain closes connections that win after the race is decided.
	drain := func() {
		go func(left int) {
			for ; left > 0; left-- {
				if r := <-results; r.err == nil {
					r.conn.Close()
				}
			}
		}(pending)
	}
	if canStart() {
		startNext()
	}
	stagger := time.NewTimer(routeStagger)
	defer stagger.Stop()
	for pending > 0 {
		select {
		case r := <-results:
			pending--
			if p.reserveRoute(r.i) {
				reservePending--
			}
			if r.err == nil {
				p.mu.Lock()
				allowed := p.matches(r.i, p.only)
				p.mu.Unlock()
				if !allowed {
					r.conn.Close()
					if canStart() {
						startNext()
						stagger.Reset(routeStagger)
					}
					continue
				}
				stop()
				drain()
				p.mark(r.i, false)
				p.established.Store(true)
				conn := net.Conn(&poolConn{Conn: r.conn, pool: p, index: r.i, network: p.network, address: address})
				if r.i < len(p.kinds) {
					conn = viaConn{conn, p.kinds[r.i]}
				}
				return conn, nil
			}
			if ctx.Err() != nil {
				drain()
				return nil, ctx.Err()
			}
			if observe, ok := ctx.Value(connectionObserverKey{}).(func(string)); ok && !background {
				observe(p.routeName(r.i))
			}
			if errors.Is(r.err, errDestinationRefused) {
				refusals++
				p.markRefused(r.i, address, errors.Is(r.err, errDestinationUnreachable))
			}
			if canStart() {
				startNext()
				stagger.Reset(routeStagger)
			}
		case <-stagger.C:
			if canStart() {
				startNext()
				stagger.Reset(routeStagger)
			}
		case <-ctx.Done():
			stop()
			drain()
			return nil, ctx.Err()
		}
	}
	if refusals == started && (refusals > 0 || len(order) < len(available)) {
		return nil, errDestinationRefused
	}
	p.established.Store(false)
	if p.onFailure != nil {
		p.onFailure()
	}
	return nil, errors.New("all configured routes failed")
}

func refusedKey(i int, address string) string { return strconv.Itoa(i) + "|" + address }

// withoutRefused drops the routes that refused address recently.
func (p *routePool) withoutRefused(order []int, address string) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	out := make([]int, 0, len(order))
	for _, i := range order {
		if !now.Before(p.refused[refusedKey(i, address)]) {
			out = append(out, i)
		}
	}
	return out
}

func (p *routePool) markRefused(i int, address string, unreachable bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	if p.refused == nil {
		p.refused = map[string]time.Time{}
	}
	if len(p.refused) >= 1024 {
		for k, until := range p.refused {
			if !now.Before(until) {
				delete(p.refused, k)
			}
		}
	}
	skip := refusedFor
	if unreachable {
		skip = unreachableFor
	}
	p.refused[refusedKey(i, address)] = now.Add(skip)
}

// order lists the routes that can carry ip in priority order, the ones that
// failed recently last.
func (p *routePool) order(ip net.IP) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	p.tickLocked(now)
	var ready, reserve, cooling, stalled []int
	for i := range p.clients {
		if ip != nil && ip.To4() == nil && !p.ipv6[i] || !p.matches(i, p.only) {
			continue
		}
		if p.stalledLocked(p.network, i) {
			stalled = append(stalled, i)
		} else if now.Before(p.failedUntil[i]) {
			cooling = append(cooling, i)
		} else if p.reserveRoute(i) {
			reserve = append(reserve, i)
		} else {
			ready = append(ready, i)
		}
	}
	return append(append(append(ready, reserve...), cooling...), stalled...)
}

func (p *routePool) reserveRoute(i int) bool { return i < len(p.uses) && p.uses[i] == "reserve" }

// matches is called with p.mu held.
func (p *routePool) matches(i int, selection string) bool {
	if selection == "" {
		return true
	}
	if id, ok := strings.CutPrefix(selection, "route:"); ok {
		return i < len(p.ids) && p.ids[i] == id && p.kinds[i] != "my"
	}
	return p.kinds[i] == selection
}

// restrict limits new connections to a type or route ID ("" = all).
func (p *routePool) restrict(selection string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	found := selection == ""
	for i := range p.clients {
		found = found || p.matches(i, selection)
	}
	if !found {
		return false
	}
	p.only = selection
	return true
}

func (p *routePool) mark(i int, failed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !failed {
		delete(p.failedUntil, i)
		return
	}
	if p.failedUntil == nil {
		p.failedUntil = map[int]time.Time{}
	}
	p.failedUntil[i] = p.clock().Add(routeCooldown)
}

// Route states in Status.Tunnels.
const (
	routeUp     = "up"     // has a working connection
	routeFailed = "failed" // failed recently; new connections try it last
	routeIdle   = "idle"   // not started yet, or released when unused
)

// RouteState is one configured tunnel route in Status.
type RouteState struct {
	Exit   string `json:"exit,omitempty"`
	Use    string `json:"use,omitempty"`
	ID     string `json:"id"`
	Type   string `json:"type"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Stalls int64  `json:"stalls"`
}

func (p *routePool) states() []RouteState {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	out := make([]RouteState, len(p.clients))
	for i, c := range p.clients {
		out[i].State = routeIdle
		if i < len(p.exits) {
			out[i].Exit = p.exits[i]
		}
		if i < len(p.uses) {
			out[i].Use = p.uses[i]
		}
		pinnedFailure := strings.HasPrefix(p.only, "route:") && p.matches(i, p.only) && !p.failedUntil[i].IsZero()
		switch {
		case now.Before(p.failedUntil[i]) || pinnedFailure || p.stalledLocked(p.network, i):
			out[i].State = routeFailed
		case c.Established():
			out[i].State = routeUp
		}
		if h := p.stalls[p.network][i]; h != nil {
			out[i].Stalls = h.count
			if h.blocked() {
				out[i].Reason = "stalled"
			}
		}
		if i < len(p.ids) {
			out[i].ID, out[i].Type = p.ids[i], p.kinds[i]
		}
	}
	return out
}

// tunnelWay is one tunnel route as a column of the network test.
type tunnelWay struct {
	exit   string
	name   string
	dial   dialFunc
	ipv6   bool
	server routeServer
}

// ways names each route by its type, or by its ID when two share a type.
func (p *routePool) ways() []tunnelWay {
	count := map[string]int{}
	for _, k := range p.kinds {
		count[k]++
	}
	out := make([]tunnelWay, len(p.clients))
	for i, c := range p.clients {
		name := p.kinds[i]
		if count[name] > 1 {
			name = p.ids[i]
		}
		out[i] = tunnelWay{name: name, dial: c.DialContext, ipv6: p.ipv6[i]}
		if i < len(p.exits) {
			out[i].exit = p.exits[i]
		}
		if i < len(p.servers) {
			out[i].server = p.servers[i]
		}
	}
	return out
}

// viaConn records which kind of route carries a tunnel connection.
type viaConn struct {
	net.Conn
	via string
}

func (c viaConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

func connVia(c net.Conn) string {
	if v, ok := c.(viaConn); ok {
		return v.via
	}
	return ""
}

func (p *routePool) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}
func (p *routePool) Established() bool { return p.established.Load() }
func (p *routePool) PathStats() []soar.PathStat {
	var out []soar.PathStat
	for _, c := range p.clients {
		out = append(out, c.PathStats()...)
	}
	return out
}
func (p *routePool) Close() error {
	for _, c := range p.clients {
		c.Close()
	}
	return nil
}

type irohClient struct {
	mu          sync.Mutex
	route       links.Route
	binary, dir string
	dns         []string
	dial        dialFunc
	cleanup     func()
	closed      bool
	generation  uint64
	ready       atomic.Bool
	now         func() time.Time
	onRestart   func(time.Time)
	start       func(context.Context) (dialFunc, func(), error)
	failures    int
	lastRestart time.Time
}

func (c *irohClient) DialContext(ctx context.Context, n, a string) (net.Conn, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, net.ErrClosed
	}
	if c.dial == nil {
		if c.dir == "" {
			c.mu.Unlock()
			return nil, errors.New("iroh storage is unavailable")
		}
		if err := os.MkdirAll(c.dir, 0700); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		life, cancel := context.WithCancel(context.Background())
		start := c.start
		if start == nil {
			start = func(ctx context.Context) (dialFunc, func(), error) {
				return startIroh(ctx, c.route, c.binary, c.dir, c.dns)
			}
		}
		dial, cleanup, err := start(life)
		if err != nil {
			cancel()
			c.mu.Unlock()
			return nil, err
		}
		c.generation++
		c.dial = dial
		c.cleanup = func() { cancel(); cleanup() }
	}
	generation := c.generation
	dial := c.dial
	c.mu.Unlock()
	conn, err := dial(ctx, n, a)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation == generation && c.dial != nil && !c.closed {
		c.ready.Store(err == nil)
		restart := false
		now := time.Now()
		if c.now != nil {
			now = c.now()
		}
		if err == nil {
			c.failures = 0
		} else if !errors.Is(err, errDestinationRefused) && !errors.Is(err, context.Canceled) {
			c.failures++
			restart = c.failures >= 3 && (c.lastRestart.IsZero() || now.Sub(c.lastRestart) >= 5*time.Minute)
		}
		if errors.Is(err, errIrohExited) || restart {
			if c.cleanup != nil {
				c.cleanup()
			}
			c.cleanup = nil
			c.dial = nil
			c.ready.Store(false)
			c.failures = 0
			if restart {
				c.lastRestart = now
				if c.onRestart != nil {
					c.onRestart(now)
				}
			}
		}
	}
	return conn, err
}
func (c *irohClient) Established() bool          { return c.ready.Load() }
func (c *irohClient) PathStats() []soar.PathStat { return nil }
func (c *irohClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.cleanup != nil {
		c.cleanup()
		c.cleanup = nil
	}
	c.dial = nil
	c.ready.Store(false)
	return nil
}

// poolConn keeps feedback attached to the route that won the dial race.
type poolConn struct {
	net.Conn
	pool    *routePool
	index   int
	network string
	address string
}

func (c *poolConn) feedback(stalled bool, first ...bool) {
	p := c.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stalls == nil {
		p.stalls = map[string]map[int]*hold{}
	}
	if p.stalls[c.network] == nil {
		p.stalls[c.network] = map[int]*hold{}
	}
	h := p.stalls[c.network][c.index]
	if h == nil {
		h = &hold{}
		p.stalls[c.network][c.index] = h
	}
	defer func() { p.rememberHoldLocked(c.index, h, p.clock()) }()
	if stalled {
		pinned := strings.HasPrefix(p.only, "route:")
		corroborated := h.onStallReason(p.clock(), p.log, p.routeName(c.index), "first reply, 2 destinations", len(first) == 0 || first[0], c.address, pinned)
		if pinned || !corroborated {
			return
		}
		p.tickLocked(p.clock())
		all := len(p.clients) > 0
		for i := range p.clients {
			if p.matches(i, p.only) && !p.stalledLocked(p.network, i) && !p.clock().Before(p.failedUntil[i]) {
				all = false
				break
			}
		}
		if all && p.onFailure != nil {
			p.onFailure()
		}
	} else {
		h.onReply(p.log, p.routeName(c.index))
		delete(p.failedUntil, c.index)
	}
}
func (c *poolConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}
func (p *routePool) stalledLocked(network string, i int) bool {
	h := p.stalls[network][i]
	return h.blocked()
}
func poolFeedback(c net.Conn, stalled bool, first ...bool) {
	if v, ok := c.(viaConn); ok {
		c = v.Conn
	}
	if v, ok := c.(*poolConn); ok {
		v.feedback(stalled, first...)
	}
}

func (p *routePool) routeName(i int) string {
	if i >= len(p.ids) {
		return "tunnel"
	}
	if i < len(p.exits) {
		return routeDisplayName(p.ids[i], p.exits[i])
	}
	return p.ids[i]
}

func routeDisplayName(id, exit string) string {
	if exit != "" {
		return id + " (exit=" + exit + ")"
	}
	return id
}

func (p *routePool) tick(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tickLocked(now)
}

func (p *routePool) tickLocked(now time.Time) {
	for _, holds := range p.stalls {
		for i, h := range holds {
			h.tick(now, p.log, p.routeName(i))
			p.rememberHoldLocked(i, h, now)
		}
	}
}
