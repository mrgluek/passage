package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
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
	if t.cfg.BrowserListen == "" {
		return
	}
	host, _, err := net.SplitHostPort(t.cfg.BrowserListen)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		t.log.add("browser listener: the address must be 127.0.0.1 and a port")
		return
	}
	ln, err := net.Listen("tcp", t.cfg.BrowserListen)
	if err != nil {
		t.log.add("browser listener: %v", err)
		return
	}
	if ln.Addr().String() == t.ln.Addr().String() {
		ln.Close()
		t.log.add("browser listener: same address as the proxy")
		return
	}
	t.browserLn = ln
	t.pac = buildPAC(t.sites.hosts, ln.Addr().(*net.TCPAddr).Port)
	t.log.add("browser listener started: %d sites", len(t.sites.hosts))
	t.wg.Add(1)
	go t.serveBrowser(ln)
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
		return
	}
	host, _, _ := net.SplitHostPort(addr)
	entry := t.sites.match(host)
	if entry == nil || net.ParseIP(host) != nil {
		socksReply(c, repNotAllowed)
		return
	}
	c.SetDeadline(time.Time{})
	t.connectSite(c, entry, host, addr)
}

// connectSite carries one browser connection. It keeps its own counters and
// per-site route memory; it never reaches the groups, probes or stall statistics.
func (t *Tunnel) connectSite(c bufferedConn, entry *siteEntry, host, addr string) {
	remote, route, err := t.dialSite(entry, addr)
	if err != nil {
		t.log.add("site dial failed: %s", maskSite(err.Error(), host))
		socksReply(c, repHostUnreachable)
		return
	}
	defer remote.Close()
	// Close drops the remote side too: a direct server may ignore a half-close.
	t.mu.Lock()
	if t.ctx.Err() != nil {
		t.mu.Unlock()
		return
	}
	if t.siteRemotes == nil {
		t.siteRemotes = map[net.Conn]struct{}{}
	}
	t.siteRemotes[remote] = struct{}{}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.siteRemotes, remote)
		t.mu.Unlock()
	}()
	if err := socksReply(c, repSucceeded); err != nil {
		return
	}
	t.siteTotal.Add(1)
	t.siteOpen.Add(1)
	defer t.siteOpen.Add(-1)
	watched := watchResponsesKeep(remote, func() { entry.clear(route) }, func(first bool) {
		if first {
			entry.markBad(route, t.timeNow().Add(siteBadFor))
			t.log.add("site: no reply on %s, trying another route next time", route)
		}
		c.Close()
	}, func() bool { return true })
	defer watched.Close()
	var up, down atomic.Int64
	done := make(chan struct{}, 2)
	go func() { pipe(watched, c, &up, &t.upBytes[route]); done <- struct{}{} }()
	go func() { pipe(c, watched, &down, &t.downBytes[route]); done <- struct{}{} }()
	<-done
	<-done
}

// dialSite tries the routes the site's kind allows: those that have not
// failed lately first, the others last. A refused destination is not a failure
// of the route.
func (t *Tunnel) dialSite(entry *siteEntry, addr string) (net.Conn, Route, error) {
	t.mu.Lock()
	ways := t.ways
	t.mu.Unlock()
	now := t.timeNow()
	var good, late []Route
	mode := t.mode
	for _, r := range []Route{RouteDirect, RouteSmart, RouteTunnel} {
		switch {
		case ways[r].dial == nil,
			r == RouteTunnel && (!entry.tunnel || mode == ModeOff),
			r != RouteTunnel && entry.tunnel && mode == ModeAlways: // the chosen route only
			continue
		}
		if entry.badUntil(r).After(now) {
			late = append(late, r)
		} else {
			good = append(good, r)
		}
	}
	var last error
	for _, r := range append(good, late...) {
		ctx, cancel := context.WithTimeout(t.ctx, ways[r].timeout)
		dial := ways[r].dial
		if pool, ok := t.client.(*routePool); ok && r == RouteTunnel {
			dial = pool.siteDialContext // a failure stays with the browser connection
		}
		c, err := dial(ctx, "tcp", addr)
		cancel()
		if err == nil {
			return c, r, nil
		}
		last = err
		if !errors.Is(err, errDestinationRefused) && t.ctx.Err() == nil {
			entry.markBad(r, now.Add(siteBadFor))
		}
	}
	if last == nil {
		last = errors.New("no route available")
	}
	return nil, RouteDirect, last
}

// maskSite removes a site's name from text before it reaches the logs.
func maskSite(text, host string) string {
	for _, h := range []string{host, strings.ToLower(host), strings.ToUpper(host)} {
		text = strings.ReplaceAll(text, h, "<site>")
	}
	return text
}

// browserStatus is nil while the listener is off.
func (t *Tunnel) browserStatus() *BrowserStatus {
	if t.browserLn == nil {
		return nil
	}
	return &BrowserStatus{Addr: t.BrowserAddr(), PAC: t.PACURL(), Hosts: len(t.sites.hosts), Open: t.siteOpen.Load(), Total: t.siteTotal.Load()}
}
