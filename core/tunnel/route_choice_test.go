package tunnel

import (
	"context"
	"errors"
	"net"
	"testing"

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
