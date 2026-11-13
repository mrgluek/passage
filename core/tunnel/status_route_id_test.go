package tunnel

import (
	"net"
	"testing"
)

func TestStatusRouteID(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	p := &routePool{ids: []string{"berry"}}
	c := viaConn{&poolConn{Conn: a, pool: p, index: 0}, "iroh"}
	if got := connRouteID(c); got != "berry" {
		t.Fatal(got)
	}
	if got := connRouteID(a); got != "" {
		t.Fatal(got)
	}
	tun := &Tunnel{mode: ModeAuto}
	g := &group{route: RouteTunnel, routeID: connRouteID(c)}
	if got := tun.groupRouteIDLocked(g); got != "berry" {
		t.Fatal(got)
	}
	tun.only = "route:pinned"
	if got := tun.groupRouteIDLocked(g); got != "pinned" {
		t.Fatal(got)
	}
	g.route = RouteDirect
	if got := tun.groupRouteIDLocked(g); got != "" {
		t.Fatal(got)
	}
}
