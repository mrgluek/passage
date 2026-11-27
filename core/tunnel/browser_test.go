package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newBrowserTunnel is newTestTunnel with a browser listener on a free port.
func newBrowserTunnel(t *testing.T, groups []SiteGroup, direct, tun dialFunc) *Tunnel {
	t.Helper()
	tt := newTestTunnel(t, ModeAuto, direct, tun)
	tt.cfg.Sites = groups
	tt.cfg.BrowserListen = "127.0.0.1:0"
	tt.startBrowser()
	if tt.browserLn == nil {
		t.Fatal("browser listener did not start")
	}
	return tt
}

var testSites = []SiteGroup{{Via: "smart", Hosts: []string{"example.org"}}, {Via: "tunnel", Hosts: []string{"example.net"}}}

// browserSocks is a no-auth SOCKS5 CONNECT to the browser listener.
func browserSocks(t *testing.T, tt *Tunnel, host string, ip []byte) (net.Conn, byte) {
	t.Helper()
	c, err := net.Dial("tcp", tt.BrowserAddr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte{5, 1, 0})
	r := make([]byte, 2)
	if _, err := io.ReadFull(c, r); err != nil || r[1] != 0 {
		t.Fatalf("no-auth method refused: %v %v", r, err)
	}
	req := []byte{5, 1, 0}
	if ip != nil {
		req = append(append(req, 1), ip...)
	} else {
		req = append(append(req, 3, byte(len(host))), host...)
	}
	c.Write(append(req, 1, 187))
	rep := make([]byte, 10)
	if _, err := io.ReadFull(c, rep); err != nil {
		t.Fatal(err)
	}
	return c, rep[1]
}

func httpGet(t *testing.T, tt *Tunnel, method, path, host string) (*http.Response, string) {
	t.Helper()
	c, err := net.Dial("tcp", tt.BrowserAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: %s\r\n\r\n", method, path, host)
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestPACListsExactlyTheSites(t *testing.T) {
	var calls atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &calls), echoDialer("t:", &calls))
	resp, body := httpGet(t, tt, "GET", "/proxy.pac", tt.BrowserAddr())
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ns-proxy-autoconfig" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS header")
	}
	_, port, _ := net.SplitHostPort(tt.BrowserAddr())
	for _, want := range []string{`"example.org":1`, `"example.net":1`, "SOCKS5 127.0.0.1:" + port + "; SOCKS 127.0.0.1:" + port + "; DIRECT", "isPlainHostName"} {
		if !strings.Contains(body, want) {
			t.Errorf("PAC lacks %q:\n%s", want, body)
		}
	}
	if strings.Count(body, ":1") != 2 || strings.Contains(body, "telegram") {
		t.Fatalf("PAC lists other hosts:\n%s", body)
	}
	if got := httpGetLocalhost(t, tt); got != 200 {
		t.Fatalf("localhost Host: %d", got)
	}
}

func httpGetLocalhost(t *testing.T, tt *Tunnel) int {
	_, port, _ := net.SplitHostPort(tt.BrowserAddr())
	resp, _ := httpGet(t, tt, "GET", "/proxy.pac", "localhost:"+port)
	return resp.StatusCode
}

func TestPACRefusesWrongHostPathAndMethod(t *testing.T) {
	var calls atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &calls), echoDialer("t:", &calls))
	_, port, _ := net.SplitHostPort(tt.BrowserAddr())
	for _, c := range []struct{ method, path, host string }{
		{"GET", "/proxy.pac", "rebind.example.org:" + port},
		{"GET", "/proxy.pac", "127.0.0.1"},
		{"GET", "/proxy.pac", "127.0.0.1:1"},
		{"GET", "/", "127.0.0.1:" + port},
		{"GET", "/proxy.pac/x", "127.0.0.1:" + port},
		{"POST", "/proxy.pac", "127.0.0.1:" + port},
	} {
		if resp, body := httpGet(t, tt, c.method, c.path, c.host); resp.StatusCode != 404 || strings.Contains(body, "FindProxyForURL") {
			t.Errorf("%+v: %d", c, resp.StatusCode)
		}
	}
}

func TestBrowserSocksOnlyForListedHosts(t *testing.T) {
	var calls atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &calls), echoDialer("t:", &calls))
	for _, host := range []string{"example.org", "www.example.org", "a.b.example.org", "EXAMPLE.org"} {
		c, rep := browserSocks(t, tt, host, nil)
		if rep != 0 {
			t.Fatalf("%s refused: %d", host, rep)
		}
		if got := roundTrip(t, c); got != "d:hi" {
			t.Fatalf("%s: %q", host, got)
		}
	}
	for _, host := range []string{"badexample.org", "example.org.evil.test", "org", "other.example.com", "example.network"} {
		if _, rep := browserSocks(t, tt, host, nil); rep != repNotAllowed {
			t.Errorf("%s: reply %d", host, rep)
		}
	}
	if _, rep := browserSocks(t, tt, "", []byte{93, 184, 216, 34}); rep != repNotAllowed {
		t.Errorf("IP literal: reply %d", rep)
	}
	if calls.Load() != 4 {
		t.Fatalf("refused hosts were dialed: %d calls", calls.Load())
	}
}

func TestMainPortStillNeedsAuth(t *testing.T) {
	var calls atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &calls), echoDialer("t:", &calls))
	c, err := net.Dial("tcp", tt.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte{5, 1, 0})
	r := make([]byte, 2)
	io.ReadFull(c, r)
	if r[1] != 0xff {
		t.Fatalf("main port offered method %d", r[1])
	}
}

func TestBrowserListenerOffByDefault(t *testing.T) {
	var calls atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("d:", &calls), echoDialer("t:", &calls))
	tt.startBrowser()
	if tt.BrowserAddr() != "" || tt.PACURL() != "" || tt.browserStatus() != nil {
		t.Fatal("listener started without BrowserListen")
	}
	tt.cfg.BrowserListen = "0.0.0.0:0"
	tt.startBrowser()
	if tt.BrowserAddr() != "" {
		t.Fatal("non-loopback address accepted")
	}
}

// Site hosts never become relay groups, probe targets or state.
func TestSiteHostsStayOutOfTheGroups(t *testing.T) {
	var calls atomic.Int32
	tt := newBrowserTunnel(t, []SiteGroup{{Via: "tunnel", Hosts: []string{"web.telegram.org", "example.net"}}}, echoDialer("d:", &calls), echoDialer("t:", &calls))
	for _, host := range []string{"web.telegram.org", "kws2.web.telegram.org", "example.net"} {
		c, rep := browserSocks(t, tt, host, nil)
		if rep != 0 {
			t.Fatal(host, rep)
		}
		roundTrip(t, c)
	}
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if len(tt.relays) != 0 || len(tt.telegram.hosts) != 0 || tt.telegram.seen || tt.other.seen {
		t.Fatalf("site hosts reached the groups: relays=%d telegram=%v", len(tt.relays), tt.telegram.hosts)
	}
	for _, g := range tt.groupsLocked() {
		if g.state != "unknown" || g.route != RouteDirect || g.stalls[RouteDirect] != nil {
			t.Fatalf("group %s changed: %s %s", g.name, g.state, g.route)
		}
		if got := tt.targetsLocked(g); len(got) != 0 {
			t.Fatalf("probe targets from sites: %v", got)
		}
	}
	if tt.stats[RouteDirect].Conns != 0 || tt.stats[RouteTunnel].Conns != 0 {
		t.Fatal("site connections counted in route stats")
	}
}

// A burst of idle browser sockets leaves route and ban state alone.
func TestIdleSiteBurstChangesNoState(t *testing.T) {
	var calls atomic.Int32
	old := firstResponseTimeout
	firstResponseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { firstResponseTimeout = old })
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &calls), echoDialer("t:", &calls))
	before := tt.Status(0)
	var conns []net.Conn
	for i := 0; i < 40; i++ {
		c, rep := browserSocks(t, tt, "www.example.org", nil)
		if rep != 0 {
			t.Fatal(rep)
		}
		conns = append(conns, c) // never writes: a preconnect
	}
	time.Sleep(200 * time.Millisecond)
	for _, c := range conns {
		c.Close()
	}
	after := tt.Status(0)
	if after.State != before.State || after.Route != before.Route || after.Health.State != before.Health.State {
		t.Fatalf("state moved: %s/%s -> %s/%s", before.State, before.Route, after.State, after.Route)
	}
	if after.Stats["direct"].Conns != 0 || after.Stats["direct"].Fails != 0 || after.Stats["tunnel"].Fails != 0 {
		t.Fatalf("stats moved: %+v", after.Stats)
	}
	tt.mu.Lock()
	defer tt.mu.Unlock()
	for _, g := range tt.groupsLocked() {
		if g.failStreak != 0 || g.stalls[RouteDirect] != nil || g.stalls[RouteTunnel] != nil {
			t.Fatalf("group %s took a ban", g.name)
		}
	}
	if tt.sites.match("example.org").badUntil(RouteDirect).After(time.Now()) {
		t.Fatal("idle sockets marked the route bad")
	}
}

func TestSmartSitesNeverUseTheTunnel(t *testing.T) {
	var directCalls, tunnelCalls atomic.Int32
	tt := newBrowserTunnel(t, testSites, failDialer(&directCalls), echoDialer("t:", &tunnelCalls))
	if _, rep := browserSocks(t, tt, "example.org", nil); rep != repHostUnreachable {
		t.Fatalf("reply %d", rep)
	}
	if tunnelCalls.Load() != 0 {
		t.Fatal("a smart site used the tunnel")
	}
	// A tunnel site falls through to it.
	c, rep := browserSocks(t, tt, "example.net", nil)
	if rep != 0 || tunnelCalls.Load() != 1 {
		t.Fatalf("tunnel site: reply %d, tunnel calls %d", rep, tunnelCalls.Load())
	}
	if got := roundTrip(t, c); got != "t:hi" {
		t.Fatal(got)
	}
}

func TestSiteSmartRouteIsUsedWhenItWorks(t *testing.T) {
	var d, s, tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, failDialer(&d), echoDialer("t:", &tu))
	tt.ways[RouteSmart] = way{echoDialer("s:", &s), time.Second}
	c, rep := browserSocks(t, tt, "example.org", nil)
	if rep != 0 || roundTrip(t, c) != "s:hi" || tu.Load() != 0 {
		t.Fatalf("reply %d, tunnel calls %d", rep, tu.Load())
	}
}

func TestRefusedSiteDoesNotMarkTheRouteBad(t *testing.T) {
	var tunnelCalls atomic.Int32
	refuse := func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("exit: %w", errDestinationRefused)
	}
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &tunnelCalls), refuse)
	tt.ways[RouteDirect] = way{refuse, time.Second}
	if _, rep := browserSocks(t, tt, "example.net", nil); rep != repHostUnreachable {
		t.Fatalf("reply %d", rep)
	}
	e := tt.sites.match("example.net")
	if e.badUntil(RouteDirect).After(time.Now()) || e.badUntil(RouteTunnel).After(time.Now()) {
		t.Fatal("a refused destination marked a route bad")
	}
}

// Silence after the first request moves the next connection to another route.
func TestSiteStallPrefersAnotherRoute(t *testing.T) {
	old := firstResponseTimeout
	firstResponseTimeout = 100 * time.Millisecond
	t.Cleanup(func() { firstResponseTimeout = old })
	var tunnelCalls atomic.Int32
	silent := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go io.Copy(io.Discard, b)
		return a, nil
	}
	tt := newBrowserTunnel(t, testSites, silent, echoDialer("t:", &tunnelCalls))
	c, rep := browserSocks(t, tt, "example.net", nil)
	if rep != 0 {
		t.Fatal(rep)
	}
	c.Write([]byte("hi"))
	io.Copy(io.Discard, c) // the stall closes it
	c2, rep := browserSocks(t, tt, "example.net", nil)
	if rep != 0 || roundTrip(t, c2) != "t:hi" {
		t.Fatalf("second connection did not use the tunnel (reply %d)", rep)
	}
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if tt.other.route != RouteDirect || tt.telegram.route != RouteDirect || tt.stats[RouteDirect].Fails != 0 {
		t.Fatal("group route or stats moved")
	}
}

func TestSiteNamesAreMaskedInLogs(t *testing.T) {
	var calls atomic.Int32
	fail := func(_ context.Context, _, addr string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("dial tcp: lookup " + addr + ": no such host")
	}
	tt := newBrowserTunnel(t, []SiteGroup{{Via: "smart", Hosts: []string{"private-site.example.org"}}}, fail, nil)
	tt.ways[RouteTunnel] = way{}
	if _, rep := browserSocks(t, tt, "private-site.example.org", nil); rep != repHostUnreachable {
		t.Fatal(rep)
	}
	events := tt.Status(50).Events
	found := false
	for _, e := range events {
		if strings.Contains(e.Msg, "private-site") {
			t.Fatalf("site name in log: %s", e.Msg)
		}
		found = found || strings.Contains(e.Msg, "site dial failed")
	}
	if !found {
		t.Fatal("failure not logged")
	}
	st := tt.Status(1).Browser
	if st == nil || st.Hosts != 1 || strings.Contains(fmt.Sprint(st), "private-site") {
		t.Fatalf("status: %+v", st)
	}
}

func TestSiteMatching(t *testing.T) {
	s := newSiteSet([]SiteGroup{{Via: "smart", Hosts: []string{"example.org", "Bad Host", "10.0.0.1", "*.example.com"}}})
	for host, want := range map[string]bool{"example.org": true, "a.example.org": true, "a.b.example.org.": true, "badexample.org": false, "org": false, "example.com": false, "10.0.0.1": false, "": false} {
		if (s.match(host) != nil) != want {
			t.Errorf("%q: %v", host, !want)
		}
	}
	if len(s.hosts) != 1 {
		t.Fatalf("invalid hosts kept: %v", s.hosts)
	}
}

func TestSiteGroupsMergeAndDeduplicate(t *testing.T) {
	cfg := Config{
		Sites: []SiteGroup{{Via: "smart", Hosts: []string{"example.org", "Example.net"}}, {Via: "ru", Hosts: []string{"ru.example.org"}}, {Via: "future", Hosts: []string{"future.example.org"}}},
		Subscriptions: []Subscription{
			{Sites: []SiteGroup{{Via: "tunnel", Hosts: []string{"example.org"}}}},
			{Sites: []SiteGroup{{Via: "smart", Hosts: []string{"example.net", "a.example.com"}}}},
		},
	}
	got := cfg.SiteGroups()
	want := []SiteGroup{{Via: "smart", Hosts: []string{"a.example.com", "example.net"}}, {Via: "tunnel", Hosts: []string{"example.org"}}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSiteModesChooseTheRoute(t *testing.T) {
	var d, tu atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &d), echoDialer("t:", &tu))
	tt.mode = ModeAlways
	c, rep := browserSocks(t, tt, "example.net", nil)
	if rep != 0 || roundTrip(t, c) != "t:hi" || d.Load() != 0 {
		t.Fatalf("always: reply %d, direct calls %d", rep, d.Load())
	}
	tt.mode = ModeOff
	c, rep = browserSocks(t, tt, "example.net", nil)
	if rep != 0 || roundTrip(t, c) != "d:hi" {
		t.Fatalf("off: reply %d", rep)
	}
}

// Close must not wait for a direct server that ignores the half-close.
func TestCloseDropsOpenSiteConnections(t *testing.T) {
	hold := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go io.Copy(io.Discard, b) // never closes
		return a, nil
	}
	var calls atomic.Int32
	tt := newBrowserTunnel(t, testSites, hold, echoDialer("t:", &calls))
	c, rep := browserSocks(t, tt, "example.org", nil)
	if rep != 0 {
		t.Fatal(rep)
	}
	c.Close()
	done := make(chan struct{})
	go func() { tt.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hangs on a site connection")
	}
}
