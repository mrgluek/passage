package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestRouteRecentRaces(t *testing.T) {
	for _, tc := range []struct {
		name    string
		first   string
		dial    func(*routePool) dialFunc
		winner  string
		started []string
		want    []*RouteRecentStats
	}{
		{
			name: "plain", first: "win", winner: "route-a", started: []string{"route-a"},
			want: []*RouteRecentStats{{Won: 1, ConnectMS: &RouteConnectMS{Median: 20, P90: 20}}, nil, nil},
		},
		{
			name: "hedged", first: "wait", winner: "route-b", started: []string{"route-a", "route-b"},
			want: []*RouteRecentStats{{Lost: 1}, {Won: 1, ConnectMS: &RouteConnectMS{Median: 20, P90: 20}}, nil},
		},
		{
			name: "fallback after failure", first: "fail", winner: "route-b", started: []string{"route-a", "route-b"},
			want: []*RouteRecentStats{{Lost: 1}, {Won: 1, ConnectMS: &RouteConnectMS{Median: 20, P90: 20}}, nil},
		},
		{
			name: "background hedge", first: "wait", winner: "route-b", started: []string{"route-a", "route-b"},
			dial: func(p *routePool) dialFunc { return p.probeDialContext },
			want: []*RouteRecentStats{nil, nil, nil},
		},
		{
			name: "app dial", first: "wait", winner: "route-b", started: []string{"route-a", "route-b"},
			dial: func(p *routePool) dialFunc {
				tt := Tunnel{client: p, mode: ModeAlways, ways: [numRoutes]way{RouteTunnel: {dial: p.DialContext}}}
				return tt.Dials()[0]
			},
			want: []*RouteRecentStats{nil, nil, nil},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := &routePool{
					ids: []string{"route-a", "route-b", "route-c"}, kinds: []string{"http", "http", "http"},
					ipv6: []bool{false, false, false},
				}
				calls := make(chan string, len(p.ids))
				for i, id := range p.ids {
					p.clients = append(p.clients, &poolFixture{dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
						calls <- id
						if i == 0 {
							switch tc.first {
							case "wait":
								<-ctx.Done()
								return nil, ctx.Err()
							case "fail":
								return nil, errors.New("route unavailable")
							}
						}
						time.Sleep(20 * time.Millisecond)
						a, b := net.Pipe()
						b.Close()
						return a, nil
					}})
				}
				dial := p.DialContext
				if tc.dial != nil {
					dial = tc.dial(p)
				}
				start := time.Now()
				c, err := dial(context.Background(), "tcp", "example.org:443")
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if got := connRouteID(c); got != tc.winner {
					t.Fatalf("winner = %q, want %q", got, tc.winner)
				}
				if tc.first == "wait" && time.Since(start) != routeStagger+20*time.Millisecond {
					t.Fatalf("hedged dial took %s", time.Since(start))
				}
				synctest.Wait()
				close(calls)
				var started []string
				for id := range calls {
					started = append(started, id)
				}
				if !slices.Equal(started, tc.started) {
					t.Fatalf("started %v, want %v", started, tc.started)
				}
				if tc.first == "fail" && !slices.Equal(p.order(nil), []int{1, 2, 0}) {
					t.Fatal("failed route should still move behind ready routes")
				}
				for i, row := range p.states() {
					if !reflect.DeepEqual(row.Recent, tc.want[i]) {
						t.Errorf("%s recent = %+v, want %+v", row.ID, row.Recent, tc.want[i])
					}
				}
			})
		})
	}
}

func TestRouteRecentWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		losses int
		want   *RouteRecentStats
	}{
		{"full", 40, &RouteRecentStats{Won: 10, Lost: 40, ConnectMS: &RouteConnectMS{Median: 55, P90: 90}}},
		{"rolled over", 41, &RouteRecentStats{Won: 9, Lost: 41, ConnectMS: &RouteConnectMS{Median: 60, P90: 100}}},
		{"no wins left", 51, &RouteRecentStats{Lost: 50}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &routePool{clients: []soarClient{&poolFixture{}, &poolFixture{}}, ids: []string{"route-a", "route-b"}, kinds: []string{"http", "http"}}
			for i := 1; i <= 10; i++ {
				p.recordRace([]int{0}, 0, time.Duration(i)*10*time.Millisecond)
			}
			for range tc.losses {
				p.recordRace([]int{0, 1}, 1, 0)
			}
			if got := p.states()[0].Recent; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("recent = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRouteRecentStatusJSON(t *testing.T) {
	tt := newTestTunnel(t, ModeAlways, nil, nil)
	p := &routePool{clients: []soarClient{&poolFixture{}, &poolFixture{}},
		ids: []string{"route-a", "route-b"}, kinds: []string{"http", "http"}}
	tt.client = p
	p.recordRace([]int{0, 1}, 0, 42*time.Millisecond)
	c := &poolConn{pool: p, index: 0, recordStats: true}
	c.feedback(true, true)
	c.feedback(true, false)
	probe := &poolConn{pool: p, index: 0}
	probe.feedback(true, false)

	b, err := json.Marshal(tt.Status(0))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Tunnels []struct {
			ID     string
			Recent json.RawMessage
		}
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if got := string(raw.Tunnels[0].Recent); got != `{"won":1,"lost":0,"connect_ms":{"median":42,"p90":42},"later":1,"stopped":1}` {
		t.Errorf("route-a recent JSON = %s", got)
	}
	if got := string(raw.Tunnels[1].Recent); got != `{"won":0,"lost":1,"later":0,"stopped":0}` {
		t.Errorf("route-b recent JSON = %s", got)
	}
	var decoded Status
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, want := decoded.Tunnels, p.states(); !reflect.DeepEqual(got, want) {
		t.Errorf("tunnels after JSON round trip = %+v, want %+v", got, want)
	}
}
