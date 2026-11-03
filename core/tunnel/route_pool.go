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
// refusedFor: apps retry at once, and each ask costs the exit work.
const refusedFor = 30 * time.Second

type routePool struct {
	clients     []soarClient
	ipv6        []bool
	kinds       []string // route type per client, reported as the connection's via
	ids         []string
	established atomic.Bool
	mu          sync.Mutex
	failedUntil map[int]time.Time
	refused     map[string]time.Time // "route index|address" -> until
	only        string               // when set, only routes of this type are tried
	now         func() time.Time     // for tests; nil = time.Now
}

func newRoutePool(cfg Config) (soarClient, error) {
	p := &routePool{}
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
		switch r.Type {
		case "my":
			c, err := newMyClient(cfg.My)
			if err != nil {
				p.Close()
				return nil, err
			}
			client = c
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
		case "wss":
			params, err := links.Params[links.WSSParams](r)
			if err != nil {
				p.Close()
				return nil, err
			}
			client, err = NewWSSClient(params)
			if err != nil {
				p.Close()
				return nil, err
			}
		case "iroh":
			client = &irohClient{route: r, binary: cfg.IrohBinary, dir: cfg.PrivateDir, dns: cfg.SystemResolvers}
		default:
			continue
		}
		p.clients = append(p.clients, client)
		p.ipv6 = append(p.ipv6, r.IPv6)
		p.kinds = append(p.kinds, r.Type)
		p.ids = append(p.ids, r.ID)
	}
	if len(p.clients) == 0 {
		return nil, errors.New("no supported routes")
	}
	return p, nil
}
func (p *routePool) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, _ := net.SplitHostPort(address)
	order := p.order(net.ParseIP(host))
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
	started, pending := 0, 0
	startNext := func() {
		n, i := started, order[started]
		started++
		pending++
		child, cancel := race, context.CancelFunc(func() {})
		if n < len(order)-1 {
			child, cancel = context.WithTimeout(race, routeAttemptTimeout)
		}
		go func() {
			conn, err := p.clients[i].DialContext(child, network, address)
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
	if len(order) > 0 {
		startNext()
	}
	stagger := time.NewTimer(routeStagger)
	defer stagger.Stop()
	for pending > 0 {
		select {
		case r := <-results:
			pending--
			if r.err == nil {
				stop()
				drain()
				p.mark(r.i, false)
				p.established.Store(true)
				conn := r.conn
				if r.i < len(p.kinds) {
					conn = viaConn{conn, p.kinds[r.i]}
				}
				return conn, nil
			}
			if ctx.Err() != nil {
				drain()
				return nil, ctx.Err()
			}
			if errors.Is(r.err, errDestinationRefused) {
				refusals++
				p.markRefused(r.i, address)
			} else {
				p.mark(r.i, true)
			}
			if started < len(order) {
				startNext()
				stagger.Reset(routeStagger)
			}
		case <-stagger.C:
			if started < len(order) {
				startNext()
				stagger.Reset(routeStagger)
			}
		case <-ctx.Done():
			stop()
			drain()
			return nil, ctx.Err()
		}
	}
	if refusals > 0 && refusals == started {
		return nil, errDestinationRefused
	}
	p.established.Store(false)
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

func (p *routePool) markRefused(i int, address string) {
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
	p.refused[refusedKey(i, address)] = now.Add(refusedFor)
}

// order lists the routes that can carry ip in priority order, the ones that
// failed recently last.
func (p *routePool) order(ip net.IP) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	var ready, cooling []int
	for i := range p.clients {
		if ip != nil && ip.To4() == nil && !p.ipv6[i] || p.only != "" && p.kinds[i] != p.only {
			continue
		}
		if now.Before(p.failedUntil[i]) {
			cooling = append(cooling, i)
		} else {
			ready = append(ready, i)
		}
	}
	return append(ready, cooling...)
}

// restrict limits new connections to routes of one type; "" allows all.
// It reports false, changing nothing, when no route has that type.
func (p *routePool) restrict(kind string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if kind != "" && !slices.Contains(p.kinds, kind) {
		return false
	}
	p.only = kind
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
	ID    string `json:"id"`
	Type  string `json:"type"`
	State string `json:"state"`
}

func (p *routePool) states() []RouteState {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	out := make([]RouteState, len(p.clients))
	for i, c := range p.clients {
		out[i].State = routeIdle
		switch {
		case now.Before(p.failedUntil[i]):
			out[i].State = routeFailed
		case c.Established():
			out[i].State = routeUp
		}
		if i < len(p.ids) {
			out[i].ID, out[i].Type = p.ids[i], p.kinds[i]
		}
	}
	return out
}

// tunnelWay is one tunnel route as a column of the network test.
type tunnelWay struct {
	name string
	dial dialFunc
	ipv6 bool
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
		out[i] = tunnelWay{name, c.DialContext, p.ipv6[i]}
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
		dial, cleanup, err := startIroh(life, c.route, c.binary, c.dir, c.dns)
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
	if errors.Is(err, errIrohExited) {
		c.mu.Lock()
		if c.generation == generation && c.cleanup != nil {
			c.cleanup()
			c.cleanup = nil
			c.dial = nil
		}
		c.mu.Unlock()
	}
	c.ready.Store(err == nil)
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
	}
	return nil
}
