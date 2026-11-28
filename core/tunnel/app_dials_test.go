package tunnel

import (
	"context"
	"errors"
	"net"
	"testing"
)

// firstDial runs the dials in order, as the update client does, and names
// the one that connected.
func firstDial(t *testing.T, dials []func(context.Context, string, string) (net.Conn, error)) (int, error) {
	t.Helper()
	var last error
	for i, d := range dials {
		c, err := d(context.Background(), "tcp", "updates.example.org:443")
		if err == nil {
			c.Close()
			return i, nil
		}
		last = err
	}
	return -1, last
}

func TestAppDialsFallBackFromPinnedRoute(t *testing.T) {
	for _, c := range []struct {
		name, mode, only string
		directOK         bool
		want             string // "direct", "route:second" or "" for a failure
	}{
		{"auto", ModeAuto, "", true, "direct"},
		{"auto direct down", ModeAuto, "", false, "route:second"},
		// without a pin the pool is the mode's own dial and serves from its other route
		{"always", ModeAlways, "", true, "route:second"},
		{"always direct down", ModeAlways, "", false, "route:second"},
		{"pinned", ModeAlways, "route:first", true, "direct"},
		{"pinned direct down", ModeAlways, "route:first", false, "route:second"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var used []string
			refuse := func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused }
			ok := func(id string) func(context.Context, string, string) (net.Conn, error) {
				return func(context.Context, string, string) (net.Conn, error) {
					used = append(used, id)
					a, b := net.Pipe()
					b.Close()
					return a, nil
				}
			}
			p := &routePool{
				clients: []soarClient{&poolFixture{dial: refuse}, &poolFixture{dial: ok("route:second")}},
				ipv6:    []bool{false, false}, kinds: []string{"wss", "wss"}, ids: []string{"first", "second"},
			}
			tu := &Tunnel{client: p, mode: c.mode}
			if c.only != "" {
				p.restrict(c.only)
				tu.only = c.only
			}
			tu.ways[RouteTunnel] = way{dial: p.DialContext}
			tu.ways[RouteDirect] = way{dial: func(context.Context, string, string) (net.Conn, error) {
				if !c.directOK {
					return nil, errors.New("down")
				}
				used = append(used, "direct")
				a, b := net.Pipe()
				b.Close()
				return a, nil
			}}
			if _, err := firstDial(t, tu.AppDials()); err != nil {
				t.Fatalf("no dial worked: %v", err)
			}
			if len(used) != 1 || used[0] != c.want {
				t.Fatalf("connected through %v, want %s", used, c.want)
			}
		})
	}
}

func TestAppDialsPinnedOnlyForAppRequests(t *testing.T) {
	p := &routePool{
		clients: []soarClient{&poolFixture{}, &poolFixture{}},
		ipv6:    []bool{false, false}, kinds: []string{"wss", "wss"}, ids: []string{"first", "second"},
	}
	p.restrict("route:first")
	if got := p.order(nil); len(got) != 1 || got[0] != 0 {
		t.Fatalf("user traffic order %v", got)
	}
	if got := p.orderFor(nil, true); len(got) != 2 {
		t.Fatalf("app order %v", got)
	}
}

func TestAppDialsRefusedByEveryRoute(t *testing.T) {
	refuse := func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused }
	p := &routePool{clients: []soarClient{&poolFixture{dial: refuse}}, ipv6: []bool{false}, kinds: []string{"wss"}, ids: []string{"first"}}
	p.restrict("route:first")
	tu := &Tunnel{client: p, mode: ModeAlways, only: "route:first"}
	tu.ways[RouteTunnel] = way{dial: p.DialContext}
	tu.ways[RouteDirect] = way{dial: refuse}
	if _, err := firstDial(t, tu.AppDials()); !errors.Is(err, ErrDestinationRefused) {
		t.Fatalf("err %v", err)
	}
}
