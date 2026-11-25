package tunnel

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func noSuchHost(context.Context, string, string) (net.Conn, error) {
	return nil, &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", IsNotFound: true}}
}

func connectOnce(t *testing.T, tt *Tunnel, addr string) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); tt.connect(a, addr, false) }()
	time.Sleep(50 * time.Millisecond)
	b.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connect did not return")
	}
}

func relayCount(tt *Tunnel) int {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return len(tt.relays)
}

func TestMissingNameMakesNoRelayGroup(t *testing.T) {
	tt := newTestTunnel(t, ModeAuto, noSuchHost, func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("no such host") })
	for _, host := range []string{"autoconfig.example.org:443", "autodiscover.example.org:443", "mail.example.org:443"} {
		connectOnce(t, tt, host)
	}
	if n := relayCount(tt); n != 0 {
		t.Fatalf("%d relay groups for names that do not exist", n)
	}
}

func TestSpoofedMissingNameStillReachedByTunnel(t *testing.T) {
	var calls testCounter
	tt := newTestTunnel(t, ModeAuto, noSuchHost, echoDialer("t:", &calls.v))
	connectOnce(t, tt, "mail.example.org:443")
	if n := relayCount(tt); n != 1 || calls.v.Load() == 0 {
		t.Fatalf("relays=%d tunnel dials=%d", n, calls.v.Load())
	}
}

func TestUnreachableNameKeepsRelayGroup(t *testing.T) {
	var d, u testCounter
	tt := newTestTunnel(t, ModeAuto, failDialer(&d.v), failDialer(&u.v))
	connectOnce(t, tt, "mail.example.org:443")
	if n := relayCount(tt); n != 1 {
		t.Fatalf("relays=%d", n)
	}
}

type testCounter struct{ v atomic.Int32 }
