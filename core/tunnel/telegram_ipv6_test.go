package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
)

func telegramIPv6Target(t *testing.T) string {
	t.Helper()
	for _, prefix := range telegramRanges {
		if prefix.Addr().Is6() {
			return net.JoinHostPort(prefix.Addr().Next().String(), "443")
		}
	}
	t.Fatal("no Telegram IPv6 range")
	return ""
}

func TestTelegramIPv6ProbesFollowLocalRoute(t *testing.T) {
	var n, ipv6Probes atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), nil)
	tt.testGroup(telegramIPv6Target(t))
	var ipv6 atomic.Bool
	tt.ipv6Route = ipv6.Load
	tt.probeMT = func(_ context.Context, addr string) error {
		host, _, _ := net.SplitHostPort(addr)
		if net.ParseIP(host).To4() == nil {
			ipv6Probes.Add(1)
			return errors.New("IPv6 unreachable")
		}
		return nil
	}
	for _, available := range []bool{false, true, false} {
		ipv6.Store(available)
		ipv6Probes.Store(0)
		tt.NetworkChanged()
		tt.check()
		g := tt.Status(0).Groups["telegram"]
		if g.Route != "direct" || g.State != "direct" || g.LastResult.CanaryChecked {
			t.Fatalf("IPv4 must keep Telegram direct: %+v", g)
		}
		if got := ipv6Probes.Load(); available && got != 1 || !available && got != 0 {
			t.Fatalf("local IPv6=%v: got %d IPv6 probes", available, got)
		}
	}
}

func TestTelegramIPv6WithoutLocalRoute(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		tunnel      bool
		localIPv6   bool
		serverIPv6  bool
		startTunnel bool
		wantReply   byte
		wantDirect  int32
		wantTunnel  int32
	}{
		{"auto with IPv6 tunnel", ModeAuto, true, false, true, false, repSucceeded, 0, 1},
		{"auto with IPv4 tunnel", ModeAuto, true, false, false, false, repNetworkUnreachable, 0, 0},
		{"auto without tunnel", ModeAuto, false, false, false, false, repNetworkUnreachable, 0, 0},
		{"never tunnel", ModeOff, true, false, true, false, repNetworkUnreachable, 0, 0},
		{"always IPv6 tunnel", ModeAlways, true, false, true, false, repSucceeded, 0, 1},
		{"always IPv4 tunnel", ModeAlways, true, true, false, false, repNetworkUnreachable, 0, 0},
		{"local IPv6", ModeAuto, true, true, false, false, repSucceeded, 1, 0},
		{"IPv4 tunnel selected", ModeAuto, true, true, false, true, repSucceeded, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var direct, tunnel atomic.Int32
			var viaTunnel dialFunc
			if tc.tunnel {
				viaTunnel = echoDialer("T:", &tunnel)
			}
			tt := newTestTunnel(t, tc.mode, echoDialer("D:", &direct), viaTunnel)
			tt.ipv6Route = func() bool { return tc.localIPv6 }
			tt.cfg.TunnelIPv6 = tc.serverIPv6
			wantRoute := initialRoute(tc.mode)
			if tc.startTunnel {
				wantRoute = RouteTunnel
				tt.telegram.route = wantRoute
			}
			c, reply := socksConnect(t, tt.Addr(), "u", "p", telegramIPv6Target(t))
			defer c.Close()
			if reply != tc.wantReply {
				t.Fatalf("SOCKS reply %d, want %d", reply, tc.wantReply)
			}
			if reply == repSucceeded {
				want := "T:hi"
				if tc.wantDirect > 0 {
					want = "D:hi"
				}
				if got := roundTrip(t, c); got != want {
					t.Fatalf("got %q, want %q", got, want)
				}
			}
			if direct.Load() != tc.wantDirect || tunnel.Load() != tc.wantTunnel {
				t.Fatalf("dials: direct=%d tunnel=%d", direct.Load(), tunnel.Load())
			}
			s := tt.Status(0)
			if s.Stats["direct"].Fails != 0 || s.Stats["tunnel"].Fails != 0 {
				t.Fatalf("local IPv6 availability must not count as a route failure: %+v", s.Stats)
			}
			if g := s.Groups["telegram"]; g.Route != wantRoute.String() {
				t.Fatalf("one IPv6 connection must not change the group route: %+v", g)
			}
		})
	}
}

func TestDiagnoseSkipsLocalIPv6ButChecksTunnel(t *testing.T) {
	for _, tc := range []struct{ local, server bool }{{false, false}, {false, true}, {true, false}, {true, true}} {
		t.Run(fmt.Sprintf("local=%v/server=%v", tc.local, tc.server), func(t *testing.T) {
			addr := fakeDC(t, resPQ)
			var d net.Dialer
			toDC := func(ctx context.Context, network, _ string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			}
			var n atomic.Int32
			tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), toDC)
			tt.cfg.Canaries = nil
			tt.ipv6Route = func() bool { return tc.local }
			tt.cfg.TunnelIPv6 = tc.server
			var directProbes atomic.Int32
			tt.probeMT = func(context.Context, string) error {
				directProbes.Add(1)
				return nil
			}
			saved := diagTelegramDCs
			diagTelegramDCs = []string{telegramIPv6Target(t)}
			t.Cleanup(func() { diagTelegramDCs = saved })
			r := tt.Diagnose(context.Background())
			if len(r.Rows) != 1 {
				t.Fatalf("rows: %+v", r.Rows)
			}
			direct, tunnel := r.Rows[0].Results[0], r.Rows[0].Results[1]
			if direct.Skip == tc.local || direct.OK != tc.local || tunnel.Skip == tc.server || tunnel.OK != tc.server {
				t.Fatalf("checks must follow each route's IPv6 support: %+v", r)
			}
			if tc.local && directProbes.Load() != 1 || !tc.local && directProbes.Load() != 0 {
				t.Fatalf("unexpected direct probes: %d", directProbes.Load())
			}
		})
	}
}

func TestIPv6FailureSkipsIPv4OnlyTunnel(t *testing.T) {
	var direct, tunnel atomic.Int32
	tt := newTestTunnel(t, ModeAuto, failDialer(&direct), echoDialer("T:", &tunnel))
	tt.ipv6Route = func() bool { return true }
	c, reply := socksConnect(t, tt.Addr(), "u", "p", telegramIPv6Target(t))
	defer c.Close()
	if reply != repHostUnreachable || direct.Load() != 1 || tunnel.Load() != 0 {
		t.Fatalf("IPv6 must not fall back to an IPv4-only server: reply=%d direct=%d tunnel=%d", reply, direct.Load(), tunnel.Load())
	}
}

func TestTunnelConfirmationFollowsServerIPv6(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			var n, ipv6Probes atomic.Int32
			tt, _ := groupsTunnel(t, echoDialer("D:", &n))
			tt.testGroup(telegramIPv6Target(t))
			tt.ipv6Route = func() bool { return true }
			tt.cfg.TunnelIPv6 = enabled
			tt.established = func() bool { return true }
			tt.probeMTTunnel = func(_ context.Context, addr string) error {
				if isIPv6Address(addr) {
					ipv6Probes.Add(1)
				}
				return nil
			}
			tt.check()
			g := tt.Status(0).Groups["telegram"]
			if g.Route != "tunnel" || !g.LastResult.TunnelOK || tt.Status(0).TunnelIPv6 != enabled {
				t.Fatalf("IPv4 confirmation must still work: %+v", g)
			}
			if enabled && ipv6Probes.Load() != 1 || !enabled && ipv6Probes.Load() != 0 {
				t.Fatalf("server IPv6=%v: got %d IPv6 probes", enabled, ipv6Probes.Load())
			}
		})
	}
}
