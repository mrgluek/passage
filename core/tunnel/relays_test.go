package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// twoRelays is a test tunnel that has seen two relays: blocked.example.org
// fails directly, open.example.org answers, and a canary answers.
func twoRelays(t *testing.T, direct dialFunc) *Tunnel {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, direct, echoDialer("T:", &n))
	tt.testGroup("blocked.example.org:993")
	tt.testGroup("open.example.org:993")
	tt.probeTLS = func(_ context.Context, hostport, _ string) error {
		if hostport == DefaultCanaries[0] || hostport == "open.example.org:993" {
			return nil
		}
		return errors.New("refused")
	}
	return tt
}

func TestEachRelayHasItsOwnRoute(t *testing.T) {
	hold := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go io.Copy(io.Discard, b)
		return a, nil
	}
	tt := twoRelays(t, hold)
	blocked, c1 := socksConnect(t, tt.Addr(), "u", "p", "blocked.example.org:993")
	open, c2 := socksConnect(t, tt.Addr(), "u", "p", "open.example.org:993")
	if c1 != repSucceeded || c2 != repSucceeded {
		t.Fatalf("connect failed: %d %d", c1, c2)
	}
	defer blocked.Close()
	defer open.Close()
	for range blockedAfter {
		tt.check()
	}
	s := tt.Status(0)
	if g := s.Groups["blocked.example.org"]; g.Route != "tunnel" || g.State != "blocked" || g.Kind != "relay" {
		t.Fatalf("the blocked relay must move to the tunnel: %+v", g)
	}
	if g := s.Groups["open.example.org"]; g.Route != "direct" || g.State != "direct" {
		t.Fatalf("the open relay must stay direct: %+v", g)
	}
	if !slices.Equal(s.Relays, []string{"blocked.example.org", "open.example.org"}) {
		t.Fatalf("relays %v", s.Relays)
	}
	blocked.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := blocked.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("the blocked relay's connection must be closed on its switch: %v", err)
	}
	open.SetDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := open.Read(make([]byte, 1)); !isTimeout(err) {
		t.Fatalf("the open relay's connection must stay: %v", err)
	}
}

func TestBlockedRelayTakesTheSmartRoute(t *testing.T) {
	var n atomic.Int32
	tt := twoRelays(t, echoDialer("D:", &n))
	var d net.Dialer
	tt.strategies = []strategy{{"works", d.DialContext}}
	tt.smartProbe = func(_ context.Context, _ dialFunc, hostport, _ string) error {
		if hostport == "blocked.example.org:993" {
			return nil
		}
		return errors.New("not needed")
	}
	for range blockedAfter {
		tt.check()
	}
	s := tt.Status(0)
	if s.Groups["blocked.example.org"].Route != "smart" || s.Groups["open.example.org"].Route != "direct" {
		t.Fatalf("blocked %+v, open %+v", s.Groups["blocked.example.org"], s.Groups["open.example.org"])
	}
}

func TestRelayGroupsAreCapped(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), nil)
	tt.mu.Lock()
	pinned := tt.relayLocked("pinned.example.org")
	pinned.pinned = true
	tt.mu.Unlock()
	for i := range maxRelayGroups + 3 {
		tt.testGroup(fmt.Sprintf("r%d.example.org:993", i))
		time.Sleep(time.Millisecond) // distinct use times
	}
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if len(tt.relays) != maxRelayGroups {
		t.Fatalf("%d relay groups, want %d", len(tt.relays), maxRelayGroups)
	}
	if tt.relays["pinned.example.org"] == nil {
		t.Error("a pinned relay must never be dropped")
	}
	if tt.relays["r0.example.org"] != nil || tt.relays[fmt.Sprintf("r%d.example.org", maxRelayGroups+2)] == nil {
		t.Error("the least recently used relay goes first, the newest stays")
	}
}
