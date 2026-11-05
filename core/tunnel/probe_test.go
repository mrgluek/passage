package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestTunnelOnlyConnectivity(t *testing.T) {
	for _, target := range []string{"relay.example.org:993", telegramDCs[0]} {
		for _, session := range []bool{false, true} {
			for _, working := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/session=%v/working=%v", target, session, working), func(t *testing.T) {
					var n atomic.Int32
					tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
					g := tt.testGroup(target)
					tt.established = func() bool { return session }
					probe := func(context.Context, string) error {
						if working {
							return nil
						}
						return errors.New("unreachable")
					}
					tt.probeMTTunnel = probe
					tt.probeTunnel = func(ctx context.Context, addr, _ string) error { return probe(ctx, addr) }
					for range blockedAfter + 1 {
						tt.check()
					}
					s := tt.Status(0).Groups[g.name]
					if s.LastResult.NetworkOK || !s.LastResult.CanaryChecked || s.LastResult.CanaryOK {
						t.Fatalf("direct connectivity must remain unavailable: %+v", s)
					}
					if working {
						if s.Route != "tunnel" || s.State != "blocked" || !s.LastResult.TunnelOK {
							t.Fatalf("a working tunnel must be used despite failed canaries: %+v", s)
						}
						c, reply := socksConnect(t, tt.Addr(), "u", "p", target)
						defer c.Close()
						if reply != repSucceeded || roundTrip(t, c) != "T:hi" {
							t.Fatal("traffic did not use the tunnel")
						}
					} else if s.Route != "direct" || s.State != "offline" {
						t.Fatalf("a session alone does not prove connectivity: %+v", s)
					}
				})
			}
		}
	}
}

func TestTunnelOnlyClosesStalledTelegramConnection(t *testing.T) {
	var n atomic.Int32
	silent := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { defer b.Close(); io.Copy(io.Discard, b) }()
		return a, nil
	}
	tt := newTestTunnel(t, ModeAuto, silent, echoDialer("T:", &n))
	tt.established = func() bool { return false }
	tt.probeMTTunnel = func(context.Context, string) error { return nil }
	c, reply := socksConnect(t, tt.Addr(), "u", "p", telegramDCs[0])
	defer c.Close()
	if reply != repSucceeded {
		t.Fatalf("SOCKS reply: %d", reply)
	}
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); tt.Status(0).Active["direct"] == 0; {
		if time.Now().After(deadline) {
			t.Fatal("connection not tracked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	gen := tt.RouteGen()
	for range blockedAfter {
		tt.check()
	}
	if _, err := c.Read(make([]byte, 1)); err == nil || (!errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET)) {
		t.Fatalf("stalled direct connection must close after switching: %v", err)
	}
	if tt.RouteGen() <= gen {
		t.Fatal("route change must notify the app")
	}
}

func TestCanariesSkippedWhenAnotherGroupAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    string
		blocked string
	}{
		{"mail answers", "relay.example.org", "telegram"},
		{"telegram answers", "telegram", "relay.example.org"},
		{"another relay answers", "other.example.org", "relay.example.org"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			tt, mailOK := groupsTunnel(t, echoDialer("D:", &n))
			mailOK.Store(false)
			var openHosts []string
			if tc.open == "telegram" {
				tt.probeMT = func(context.Context, string) error { return nil }
			} else {
				host := tc.open + ":993"
				tt.testGroup(host)
				openHosts = []string{host}
			}
			probe, seen := probeLog(openHosts...)
			tt.probeTLS = probe
			for range blockedAfter {
				tt.check()
			}
			s := tt.Status(0)
			if g := s.Groups[tc.blocked]; g.Route != "tunnel" || g.State != "blocked" {
				t.Fatalf("unreachable group must use the tunnel: %+v", g)
			}
			if g := s.Groups[tc.open]; g.Route != "direct" || g.State != "direct" {
				t.Fatalf("reachable group must stay direct: %+v", g)
			}
			for _, g := range s.Groups {
				if g.LastResult.CanaryChecked || g.LastResult.CanaryOK {
					t.Fatalf("canaries must be left alone: %+v", g)
				}
			}
			for _, host := range seen() {
				if slices.Contains(tt.cfg.Canaries, host) {
					t.Fatalf("unexpected canary probe: %s", host)
				}
			}
		})
	}
}

func TestTelegramOnlyUsesCanariesWhenUnreachable(t *testing.T) {
	for _, online := range []bool{true, false} {
		name := "offline"
		if online {
			name = "online"
		}
		t.Run(name, func(t *testing.T) {
			var n atomic.Int32
			tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
			tt.testGroup(telegramDCs[0])
			var ok []string
			if online {
				ok = []string{tt.cfg.Canaries[0]}
			}
			probe, seen := probeLog(ok...)
			tt.probeTLS = probe
			for range blockedAfter {
				tt.check()
			}
			g := tt.Status(0).Groups["telegram"]
			if !g.LastResult.CanaryChecked || g.LastResult.CanaryOK != online || len(seen()) == 0 {
				t.Fatalf("canaries must distinguish connectivity: %+v", g)
			}
			if online && (g.Route != "tunnel" || g.State != "blocked") {
				t.Fatalf("unreachable Telegram must use the tunnel: %+v", g)
			}
			if !online && (g.Route != "direct" || g.State != "offline") {
				t.Fatalf("offline must keep the route: %+v", g)
			}
		})
	}
}

func TestReachableRelaySkipsSmartSearch(t *testing.T) {
	var n atomic.Int32
	tt := twoRelays(t, echoDialer("D:", &n))
	tt.strategies = []strategy{{"works", echoDialer("S:", &n)}}
	tt.smartProbe = func(_ context.Context, _ dialFunc, hostport, _ string) error {
		if hostport != "blocked.example.org:993" {
			t.Errorf("unexpected smart probe: %s", hostport)
			return errors.New("not needed")
		}
		return nil
	}
	for range blockedAfter {
		tt.check()
	}
	s := tt.Status(0)
	if g := s.Groups["blocked.example.org"]; g.Route != "smart" || !g.LastResult.SmartOK {
		t.Fatalf("unreachable relay must use smart: %+v", g)
	}
	if g := s.Groups["open.example.org"]; g.Route != "direct" || g.LastResult.SmartChecked {
		t.Fatalf("reachable relay needs no smart search: %+v", g)
	}
}
