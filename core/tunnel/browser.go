package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	repNotAllowed         = 0x02
	maxBrowserConnections = 128
	browserRequestTimeout = 10 * time.Second
	pacPath               = "/proxy.pac"
)

// BrowserStatus is the browser listener's part of Status. It never names sites.
type BrowserStatus struct {
	Addr  string `json:"addr"`
	PAC   string `json:"pac"`
	Hosts int    `json:"hosts"`
	Open  int64  `json:"open"`
	Total int64  `json:"total"`
	// LastOK is when a site connection last got a reply from the site.
	LastOK time.Time `json:"last_ok,omitzero"`
}

// buildPAC returns the proxy auto-config for the listed hosts. A stopped
// proxy refuses the port at once, so the trailing DIRECT keeps browsers working.
func buildPAC(hosts []string, port int) []byte {
	var b strings.Builder
	b.WriteString("function FindProxyForURL(url, host) {\n")
	b.WriteString("  host = host.toLowerCase();\n  if (isPlainHostName(host)) return \"DIRECT\";\n  var sites = {")
	for i, h := range hosts {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(strconv.Quote(h) + ":1")
	}
	b.WriteString("};\n")
	addr := "127.0.0.1:" + strconv.Itoa(port)
	fmt.Fprintf(&b, "  for (var d = host; ; ) {\n    if (sites[d] === 1) return \"SOCKS5 %s; SOCKS %s; DIRECT\";\n", addr, addr)
	b.WriteString("    var i = d.indexOf(\".\");\n    if (i < 0) break;\n    d = d.substring(i + 1);\n  }\n  return \"DIRECT\";\n}\n")
	return []byte(b.String())
}

// startBrowser opens the browser listener when Config.BrowserListen is set.
// A failure is logged and leaves the router running without it.
func (t *Tunnel) startBrowser() {
	t.sites = newSiteSet(t.cfg.SiteGroups())
	t.sites.numberSites(t.cfg.siteGroupsInOrder())
	var ln net.Listener
	switch {
	case t.cfg.BrowserPort != nil:
		ln = t.cfg.BrowserPort.view()
	case t.cfg.BrowserListen == "":
		return
	default:
		host, _, err := net.SplitHostPort(t.cfg.BrowserListen)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.Equal(net.IPv4(127, 0, 0, 1)) {
			t.log.add("browser listener: the address must be 127.0.0.1 and a port")
			return
		}
		if ln, err = net.Listen("tcp", t.cfg.BrowserListen); err != nil {
			t.log.add("browser listener: %v", err)
			return
		}
	}
	if ln.Addr().String() == t.ln.Addr().String() {
		ln.Close()
		t.log.add("browser listener: same address as the proxy")
		return
	}
	t.browserLn = ln
	t.pac = buildPAC(t.sites.hosts, ln.Addr().(*net.TCPAddr).Port)
	t.log.add("browser listener started: %d sites", len(t.sites.hosts))
	t.wg.Add(2)
	go t.serveBrowser(ln)
	go t.siteSummaryLoop()
}

// BrowserAddr is the browser listener's address, empty when it is off.
func (t *Tunnel) BrowserAddr() string {
	if t.browserLn == nil {
		return ""
	}
	return t.browserLn.Addr().String()
}

// PACURL is the address browsers fetch the auto-config from.
func (t *Tunnel) PACURL() string {
	if t.browserLn == nil {
		return ""
	}
	return "http://" + t.browserLn.Addr().String() + pacPath
}

func (t *Tunnel) serveBrowser(ln net.Listener) {
	defer t.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			if t.ctx.Err() != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		t.mu.Lock()
		if t.ctx.Err() != nil || len(t.clients) >= maxConnections || t.browserOpen.Load() >= maxBrowserConnections {
			t.mu.Unlock()
			c.Close()
			continue
		}
		if t.clients == nil {
			t.clients = make(map[net.Conn]bool)
		}
		t.clients[c] = false
		t.browserOpen.Add(1)
		t.wg.Add(1)
		t.mu.Unlock()
		go func() {
			defer t.wg.Done()
			defer func() {
				t.mu.Lock()
				delete(t.clients, c)
				t.mu.Unlock()
				t.browserOpen.Add(-1)
			}()
			defer c.Close()
			t.handleBrowser(c)
		}()
	}
}

// bufferedConn reads through the buffer that peeked at the first byte.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (t *Tunnel) handleBrowser(c net.Conn) {
	c.SetDeadline(time.Now().Add(browserRequestTimeout))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	bc := bufferedConn{c, br}
	if first[0] == 0x05 {
		t.browserSOCKS(bc)
		return
	}
	t.browserHTTP(bc, br)
}

// browserHTTP answers GET /proxy.pac for a loopback Host only; everything
// else gets a bare 404 and the connection closes.
func (t *Tunnel) browserHTTP(c net.Conn, br *bufio.Reader) {
	req, err := http.ReadRequest(bufio.NewReader(io.LimitReader(br, 16<<10)))
	if err != nil {
		return
	}
	port := strconv.Itoa(t.browserLn.Addr().(*net.TCPAddr).Port)
	hostOK := req.Host == "127.0.0.1:"+port || strings.EqualFold(req.Host, "localhost:"+port)
	if req.Method != http.MethodGet || req.URL.Path != pacPath || !hostOK {
		io.WriteString(c, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/x-ns-proxy-autoconfig\r\nCache-Control: no-cache\r\nX-Content-Type-Options: nosniff\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(t.pac))
	c.Write(t.pac)
}

// browserSOCKS serves SOCKS5 without authentication, CONNECT to listed hosts only.
func (t *Tunnel) browserSOCKS(c bufferedConn) {
	addr, err := socksHandshake(c, "", "")
	if err != nil {
		if strings.HasPrefix(err.Error(), "unsupported ") {
			t.siteSocksError(repCmdUnsupported, "bad request")
		}
		return
	}
	host, _, _ := net.SplitHostPort(addr)
	entry := t.sites.match(host)
	if entry == nil || net.ParseIP(host) != nil {
		socksReply(c, repNotAllowed)
		t.siteSocksError(repNotAllowed, "host not listed")
		return
	}
	c.SetDeadline(time.Time{})
	t.connectSite(c, entry, host, addr)
}

// siteTunnelDelay is the head start direct and smart get over the tunnel for
// a via:tunnel site, as for the app's groups.
const siteTunnelDelay = 2 * time.Second

// dialSite tries the routes the site's kind allows: the one that carried the
// site last first, then those that have not failed lately, the others last.
// A refused destination is not a failure of the route.
func (t *Tunnel) dialSite(entry *siteEntry, addr string) (net.Conn, Route, []Route, error) {
	c, r, _, tried, err := t.dialSiteWay(entry, addr)
	return c, r, tried, err
}

// dialSiteWay is dialSite that also names the smart strategy it used.
func (t *Tunnel) dialSiteWay(entry *siteEntry, addr string) (net.Conn, Route, string, []Route, error) {
	t.mu.Lock()
	ways := t.ways
	strategies := t.siteStrategiesLocked()
	t.mu.Unlock()
	now := t.timeNow()
	// Smart for a site does not wait for the router to pick a strategy:
	// the site has its own choice, remembered or the first not failed.
	strat, stratBad := t.pickSiteStrategy(entry, strategies, now)
	ways[RouteSmart] = way{}
	if strat.dial != nil {
		ways[RouteSmart] = way{strat.dial, directDialTimeout}
	}
	var good, late []Route
	mode := t.mode
	tunnelOnly := entry.tunnel && mode != ModeOff && ways[RouteTunnel].dial != nil && entry.tunnelOnlyNow(now)
	for _, r := range []Route{RouteDirect, RouteSmart, RouteTunnel} {
		switch {
		case ways[r].dial == nil,
			r == RouteTunnel && (!entry.tunnel || mode == ModeOff),
			r != RouteTunnel && entry.tunnel && (mode == ModeAlways || tunnelOnly): // the chosen route only
			continue
		}
		if entry.badUntil(r).After(now) || r == RouteSmart && stratBad {
			late = append(late, r)
		} else {
			good = append(good, r)
		}
	}
	win, remembered := entry.winner(now)
	if remembered && !slices.Contains(good, win.route) {
		remembered = false
	}
	var last error
	var tried []Route
	done := func(c net.Conn, r Route) (net.Conn, Route, string, []Route, error) {
		name := ""
		if r == RouteSmart {
			name = strat.name
		}
		entry.remember(r, routeIDOf(c), t.timeNow(), name)
		return c, r, name, tried, nil
	}
	if entry.tunnel {
		for n, routes := range [][]Route{good, late} {
			if len(routes) == 0 {
				continue
			}
			// The remembered route alone gets the head start; else everything but the tunnel.
			lead := func(r Route) bool { return r != RouteTunnel }
			if n == 0 && remembered {
				lead = func(r Route) bool { return r == win.route }
			}
			c, r, err := t.raceSite(entry, routes, lead, win, remembered, ways, addr, now, &tried)
			if err == nil {
				return done(c, r)
			}
			last = err
		}
	} else {
		order := append(good, late...)
		if remembered {
			i := slices.Index(order, win.route)
			order = append([]Route{win.route}, slices.Delete(order, i, i+1)...)
		}
		for _, r := range order {
			tried = append(tried, r)
			ctx, cancel := context.WithTimeout(t.ctx, ways[r].timeout)
			c, err := t.siteDial(entry, r, ways, "")(ctx, "tcp", addr)
			cancel()
			if err == nil {
				return done(c, r)
			}
			last = err
			if remembered && r == win.route {
				entry.forget()
			}
			t.siteDialFailed(entry, r, err, now)
		}
	}
	if last == nil {
		last = errors.New("no route available")
	}
	return nil, RouteDirect, "", tried, last
}

// siteStrategiesLocked are the smart strategies a site may use: the
// router's whole list, whatever it chose for the apps. Without a list, an
// installed smart route counts as one strategy.
func (t *Tunnel) siteStrategiesLocked() []strategy {
	if len(t.strategies) > 0 {
		return t.strategies
	}
	if d := t.ways[RouteSmart].dial; d != nil {
		return []strategy{{"smart", d}}
	}
	return nil
}

// pickSiteStrategy is the site's remembered strategy while it is fresh and
// not failed, else the first one not failed lately. bad: all have failed.
func (t *Tunnel) pickSiteStrategy(entry *siteEntry, strategies []strategy, now time.Time) (strategy, bool) {
	if len(strategies) == 0 {
		return strategy{}, false
	}
	if win, ok := entry.winner(now); ok && win.route == RouteSmart {
		for _, s := range strategies {
			if s.name == win.strategy && !entry.strategyBadUntil(s.name).After(now) {
				return s, false
			}
		}
	}
	for _, s := range strategies {
		if !entry.strategyBadUntil(s.name).After(now) {
			return s, false
		}
	}
	return strategies[0], true
}

// siteDial is the dial function of a route for a site. exit, a tunnel
// route's ID, is tried first in the pool; the pool's state does not change.
func (t *Tunnel) siteDial(entry *siteEntry, r Route, ways [numRoutes]way, exit string) dialFunc {
	if r != RouteTunnel {
		// A tunnel site never asks the network's resolver: without DoT the
		// tunnel takes it. A smart site has no other way, so it may.
		return t.resolvedSiteDial(ways[r].dial, !entry.tunnel)
	}
	if pool, ok := t.client.(*routePool); ok {
		if exit == "" {
			return pool.siteDialContext // a failure stays with the browser connection
		}
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			return pool.siteDialContext(context.WithValue(ctx, preferRouteKey{}, exit), network, addr)
		}
	}
	return ways[r].dial
}

func (t *Tunnel) siteDialFailed(entry *siteEntry, r Route, err error, now time.Time) {
	// A refused destination or a DoT failure says nothing about the route.
	if !errors.Is(err, errDestinationRefused) && !errors.Is(err, errSiteDoT) && t.ctx.Err() == nil {
		entry.markBad(r, now.Add(siteBadFor))
	}
}

func (t *Tunnel) siteTimer(d time.Duration) (<-chan time.Time, func()) {
	if t.siteAfter != nil {
		return t.siteAfter(d)
	}
	tm := time.NewTimer(d)
	return tm.C, func() { tm.Stop() }
}

// raceSite dials the lead routes at once and the others after
// siteTunnelDelay or as soon as the lead ones have failed. The first connection
// wins. A losing dial still running is left to finish within its timeout,
// so a route that is blocked is remembered as bad; if it connects, it is
// closed at once.
func (t *Tunnel) raceSite(entry *siteEntry, routes []Route, lead func(Route) bool, win siteWin, remembered bool, ways [numRoutes]way, addr string, now time.Time, tried *[]Route) (net.Conn, Route, error) {
	type result struct {
		r    Route
		conn net.Conn
		err  error
	}
	results := make(chan result, len(routes))
	pending, leads, failed := 0, 0, 0
	start := func(r Route) {
		*tried = append(*tried, r)
		pending++
		ctx, cancel := context.WithTimeout(t.ctx, ways[r].timeout)
		exit := ""
		if remembered && r == win.route {
			exit = win.exit
		}
		dial := t.siteDial(entry, r, ways, exit)
		go func() {
			c, err := dial(ctx, "tcp", addr)
			cancel()
			results <- result{r, c, err}
		}()
	}
	var rest []Route // routes still waiting for their start
	for _, r := range routes {
		if lead(r) {
			start(r)
			leads++
		} else {
			rest = append(rest, r)
		}
	}
	startRest := func() {
		for _, r := range rest {
			start(r)
		}
		rest = nil
	}
	var timer <-chan time.Time
	stop := func() {}
	if leads == 0 {
		startRest()
	} else if len(rest) > 0 {
		timer, stop = t.siteTimer(siteTunnelDelay)
	}
	defer stop()
	var last error
	for pending > 0 {
		select {
		case <-timer:
			timer = nil
			startRest()
		case res := <-results:
			pending--
			if res.err == nil {
				if pending > 0 {
					go func(n int) {
						for ; n > 0; n-- {
							late := <-results
							if late.err == nil {
								late.conn.Close()
							} else {
								t.siteDialFailed(entry, late.r, late.err, now)
							}
						}
					}(pending)
				}
				return res.conn, res.r, nil
			}
			last = res.err
			if remembered && res.r == win.route {
				entry.forget()
			}
			t.siteDialFailed(entry, res.r, res.err, now)
			if lead(res.r) {
				if failed++; failed == leads {
					startRest()
				}
			}
		}
	}
	return nil, RouteDirect, last
}

// browserStatus is nil while the listener is off.
func (t *Tunnel) browserStatus() *BrowserStatus {
	if t.browserLn == nil {
		return nil
	}
	b := &BrowserStatus{Addr: t.BrowserAddr(), PAC: t.PACURL(), Hosts: len(t.sites.hosts), Open: t.siteOpen.Load(), Total: t.siteTotal.Load()}
	if n := t.siteLastOK.Load(); n != 0 {
		b.LastOK = time.Unix(0, n).UTC()
	}
	return b
}
