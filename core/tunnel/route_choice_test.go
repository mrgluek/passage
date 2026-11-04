package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

func TestRoutePoolRestrictsToOneType(t *testing.T) {
	var used []string
	fixture := func(kind string) *poolFixture {
		return &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
			used = append(used, kind)
			return nil, errors.New("down")
		}}
	}
	pool := &routePool{clients: []soarClient{fixture("wss"), fixture("iroh")}, ipv6: []bool{false, false}, kinds: []string{"wss", "iroh"}, ids: []string{"a", "b"}}
	if !pool.restrict("iroh") {
		t.Fatal("configured type refused")
	}
	pool.DialContext(context.Background(), "tcp", "example.org:443")
	if len(used) != 1 || used[0] != "iroh" {
		t.Fatalf("tried %v, want only iroh", used)
	}
	if pool.restrict("soar") || pool.only != "iroh" {
		t.Fatal("missing type accepted or changed the restriction")
	}
	used = nil
	pool.restrict("")
	pool.DialContext(context.Background(), "tcp", "example.org:443")
	if len(used) != 2 {
		t.Fatalf("tried %v, want both", used)
	}
}

func wssRoute() links.Route {
	return links.Route{ID: "web", Type: "wss", Wire: "wss-v1", Priority: 20, Params: map[string]any{"endpoint": "wss://example.org/passage", "key": wssTestKey}}
}

func TestRouteTypeAsMode(t *testing.T) {
	tt, err := Start(Config{Mode: "wss", Routes: []links.Route{wssRoute()}, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer tt.Close()
	if s := tt.Status(0); s.Mode != "wss" || s.Route != RouteTunnel.String() {
		t.Fatalf("mode %s route %s, want wss through the tunnel", s.Mode, s.Route)
	}
	if err := tt.SetMode("iroh"); err == nil || tt.Status(0).Mode != "wss" {
		t.Fatal("a type without routes must be refused and change nothing")
	}
	if err := tt.SetMode(ModeAuto); err != nil || tt.Status(0).Mode != ModeAuto || tt.client.(*routePool).only != "" {
		t.Fatal("auto must lift the restriction", err)
	}
	if err := tt.SetMode("wss"); err != nil || tt.Status(0).Mode != "wss" {
		t.Fatal("wss refused", err)
	}
}

func TestMissingRouteTypeFallsBackToAuto(t *testing.T) {
	tt, err := Start(Config{Mode: "iroh", Routes: []links.Route{wssRoute()}, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer tt.Close()
	if s := tt.Status(0); s.Mode != ModeAuto || s.Route != RouteDirect.String() {
		t.Fatalf("mode %s route %s, want auto from direct", s.Mode, s.Route)
	}
	noLink, err := Start(Config{Mode: "wss", Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer noLink.Close()
	if noLink.Status(0).Mode != ModeAuto {
		t.Fatal("a route type without a link must fall back to auto")
	}
	if _, err := (Config{Mode: "carrier-pigeon"}).Normalize(); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestRouteTypeSwitchClosesOldConnections(t *testing.T) {
	hold := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { io.Copy(io.Discard, b) }() // never answers: a long-lived connection
		return a, nil
	}
	pool := &routePool{clients: []soarClient{&poolFixture{dial: hold}, &poolFixture{dial: hold}},
		ipv6: []bool{false, false}, kinds: []string{"iroh", "wss"}, ids: []string{"a", "b"}}
	tt := newTestTunnel(t, ModeAlways, hold, pool.DialContext)
	tt.client = pool
	if err := tt.SetMode("iroh"); err != nil {
		t.Fatal(err)
	}
	c, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	if code != repSucceeded {
		t.Fatalf("connect failed: %d", code)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if via := tt.Status(0).Groups["relay.example.org"].Via; via != "iroh" {
		t.Fatalf("via %q, want iroh", via)
	}
	// The same type again keeps the connection.
	if err := tt.SetMode("iroh"); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); !isTimeout(err) {
		t.Fatalf("connection closed on the same route type: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := tt.SetMode("wss"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("iroh connection must close when wss is chosen: %v", err)
	}
	if via := tt.Status(0).Groups["relay.example.org"].Via; via == "iroh" {
		t.Fatal("status still shows iroh")
	}
	c2, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	if code != repSucceeded {
		t.Fatalf("reconnect failed: %d", code)
	}
	defer c2.Close()
	if via := tt.Status(0).Groups["relay.example.org"].Via; via != "wss" {
		t.Fatalf("via %q, want wss", via)
	}
	// Once nothing is open, no route type is shown.
	c2.Close()
	for deadline := time.Now().Add(2 * time.Second); tt.Status(0).Groups["relay.example.org"].Via != ""; {
		if time.Now().After(deadline) {
			t.Fatal("via kept after the last tunnel connection closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTelegramStartsOnTheTunnelWhereItWasBlocked(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	cfg, err := Config{Routes: []links.Route{wssRoute()}, Listen: "127.0.0.1:0", SystemResolvers: []string{"192.0.2.53:53"},
		StateFile: t.TempDir() + "/state.json"}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	rememberBlocked(cfg, "telegram", true)
	tt, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tt.Close()
	if g := tt.Status(0).Groups["telegram"]; g.Route != RouteTunnel.String() || g.State != "blocked" {
		t.Fatalf("telegram %s/%s, want the tunnel at once", g.Route, g.State)
	}
}

func TestRoutePoolSelectsOneIDWithoutFallback(t *testing.T) {
	var used []string
	fixture := func(id string) *poolFixture {
		return &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
			used = append(used, id)
			return nil, errors.New("down")
		}}
	}
	p := &routePool{clients: []soarClient{fixture("first"), fixture("second")}, ipv6: []bool{false, false}, kinds: []string{"wss", "wss"}, ids: []string{"first", "second"}}
	if !p.restrict("route:second") {
		t.Fatal("installed ID refused")
	}
	p.DialContext(context.Background(), "tcp", "example.org:443")
	if len(used) != 1 || used[0] != "second" {
		t.Fatalf("tried %v", used)
	}
	if p.restrict("route:missing") || p.only != "route:second" {
		t.Fatal("missing ID changed selection")
	}
	if !p.restrict("wss") || len(p.order(nil)) != 2 {
		t.Fatal("legacy type selection lost")
	}
}

func TestRouteIDDoesNotCollideWithMy(t *testing.T) {
	p := &routePool{clients: []soarClient{&poolFixture{}, &poolFixture{}}, ipv6: []bool{false, false}, kinds: []string{"my", "wss"}, ids: []string{"my", "my"}}
	p.restrict("route:my")
	if order := p.order(nil); len(order) != 1 || order[0] != 1 {
		t.Fatalf("ID selected %v", order)
	}
	p.restrict("my")
	if order := p.order(nil); len(order) != 1 || order[0] != 0 {
		t.Fatalf("My selected %v", order)
	}
}

func TestRouteSelectionDiscardsPendingOldConnection(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	conn, peer := net.Pipe()
	defer peer.Close()
	p := &routePool{clients: []soarClient{&poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { close(entered); <-release; return conn, nil }}, &poolFixture{}}, ipv6: []bool{false, false}, kinds: []string{"wss", "wss"}, ids: []string{"first", "second"}}
	p.restrict("route:first")
	done := make(chan error, 1)
	go func() {
		c, err := p.DialContext(context.Background(), "tcp", "example.org:443")
		if c != nil {
			c.Close()
		}
		done <- err
	}()
	<-entered
	p.restrict("route:second")
	close(release)
	if err := <-done; err == nil {
		t.Fatal("old route returned after selection changed")
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("old connection not closed: %v", err)
	}
}

func TestRouteIDModeAndConnectionSwitch(t *testing.T) {
	first, second := wssRoute(), wssRoute()
	first.ID = "first"
	second.ID = "second"
	tt, err := Start(Config{Mode: "route:first", Routes: []links.Route{first, second}, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer tt.Close()
	if tt.Status(0).Mode != "route:first" {
		t.Fatal("route ID not selected")
	}
	local, localPeer := net.Pipe()
	defer localPeer.Close()
	remote, remotePeer := net.Pipe()
	defer remotePeer.Close()
	tracked := &tracked{group: tt.other, route: RouteTunnel, local: local, remote: remote}
	tt.mu.Lock()
	tt.conns[tracked] = struct{}{}
	tt.mu.Unlock()
	if err := tt.SetMode("route:missing"); err == nil || tt.Status(0).Mode != "route:first" {
		t.Fatal("unknown ID changed mode")
	}
	if err := tt.SetMode("route:second"); err != nil {
		t.Fatal(err)
	}
	localPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := localPeer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("old local connection kept: %v", err)
	}
	remotePeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := remotePeer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("old remote connection kept: %v", err)
	}
	if tt.Status(0).Mode != "route:second" {
		t.Fatal("new ID not reported")
	}
	if err := tt.SetMode(ModeAuto); err != nil || tt.client.(*routePool).only != "" {
		t.Fatal("Auto did not lift selection")
	}
}

func TestRemovedRouteIDFallsBackToAuto(t *testing.T) {
	tt, err := Start(Config{Mode: "route:removed", Routes: []links.Route{wssRoute()}, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer tt.Close()
	if tt.Status(0).Mode != ModeAuto {
		t.Fatal("removed ID did not fall back to Auto")
	}
	for _, mode := range []string{"route:", "route:bad id", "route:UPPER"} {
		if _, err := (Config{Mode: mode, Routes: []links.Route{wssRoute()}}).Normalize(); err == nil {
			t.Fatalf("accepted %q", mode)
		}
	}
	legacy := &Tunnel{client: &poolFixture{}}
	if !legacy.restrict("route:legacy-dns") {
		t.Fatal("legacy route ID refused")
	}
}
