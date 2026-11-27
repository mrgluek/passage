package tunnel

import (
	"context"
	"errors"
	"net"
	"sync"
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

func TestNewRelayIsNotEvictedDuringFirstDial(t *testing.T) {
	release := make(chan struct{})
	slow := func(ctx context.Context, _, _ string) (net.Conn, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errors.New("filtered")
	}
	tt := newTestTunnel(t, ModeAuto, slow, slow)
	a, b := net.Pipe()
	defer b.Close()
	done := make(chan struct{})
	go func() { defer close(done); tt.connect(a, "new.example.org:443", false) }()
	time.Sleep(50 * time.Millisecond)
	tt.mu.Lock()
	for i := range maxRelayGroups + 1 {
		g := tt.relayLocked("old" + string(rune('a'+i)) + ".example.org")
		g.used = time.Now().Add(time.Hour) // newer than the group being dialed
	}
	_, kept := tt.relays["new.example.org"]
	tt.mu.Unlock()
	close(release)
	<-done
	if !kept {
		t.Fatal("a relay group was evicted during its first dial")
	}
}

func TestConcurrentFirstDialsKeepGroup(t *testing.T) {
	gate, firstGo := make(chan struct{}), make(chan struct{})
	n := 0
	var mu sync.Mutex
	direct := func(ctx context.Context, _, _ string) (net.Conn, error) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			<-firstGo
			return nil, &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", IsNotFound: true}}
		}
		<-gate
		return nil, errors.New("filtered")
	}
	tun := func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, errors.New("filtered") }
	tt := newTestTunnel(t, ModeAuto, direct, tun)
	a1, b1 := net.Pipe()
	a2, b2 := net.Pipe()
	defer b1.Close()
	defer b2.Close()
	d1, d2 := make(chan struct{}), make(chan struct{})
	go func() { defer close(d1); tt.connect(a1, "mail.example.org:443", false) }()
	time.Sleep(30 * time.Millisecond)
	go func() { defer close(d2); tt.connect(a2, "mail.example.org:443", false) }()
	time.Sleep(30 * time.Millisecond)
	close(firstGo)
	<-d1
	if relayCount(tt) != 1 {
		t.Fatal("group dropped while another first dial was running")
	}
	close(gate)
	<-d2
}
