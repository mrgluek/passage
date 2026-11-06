package tunnel

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func shortStalls(t *testing.T) {
	t.Helper()
	a, b := firstResponseTimeout, responseStallTimeout
	firstResponseTimeout, responseStallTimeout = 40*time.Millisecond, 90*time.Millisecond
	t.Cleanup(func() { firstResponseTimeout, responseStallTimeout = a, b })
}

func TestResponseWatch(t *testing.T) {
	shortStalls(t)
	for _, scenario := range []string{"silent", "delayed", "partial", "greeting", "idle", "late-write"} {
		t.Run(scenario, func(t *testing.T) {
			a, b := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer b.Close()
				switch scenario {
				case "delayed":
					io.CopyN(io.Discard, b, 1)
					timer := time.NewTimer(30 * time.Second)
					defer timer.Stop()
					closed := make(chan struct{})
					go func() { io.Copy(io.Discard, b); close(closed) }()
					select {
					case <-timer.C:
						b.Write([]byte("answer"))
					case <-closed:
					}
				case "partial":
					io.CopyN(io.Discard, b, 1)
					b.Write(bytes.Repeat([]byte("x"), 16*1024))
					io.Copy(io.Discard, b)
				case "greeting":
					b.Write([]byte("hello"))
					io.Copy(io.Discard, b)
				default:
					io.Copy(io.Discard, b)
				}
			}()
			stalled := make(chan struct{})
			var successes atomic.Int64
			c := watchResponses(a, func() { successes.Add(1) }, func() { close(stalled) })
			defer func() { c.Close(); b.Close(); <-done }()
			if scenario == "partial" {
				c.Write([]byte("x"))
			}
			if scenario == "greeting" || scenario == "partial" {
				n := 5
				if scenario == "partial" {
					n = 16 * 1024
				}
				if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "idle" || scenario == "late-write" || scenario == "greeting" {
				select {
				case <-stalled:
					t.Fatal("idle connection stalled")
				case <-time.After(2 * responseStallTimeout):
				}
				if scenario != "late-write" {
					if scenario == "greeting" && successes.Load() != 1 {
						t.Fatal("greeting was not a success")
					}
					return
				}
			}
			start := time.Now()
			if _, err := c.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			stopWrites := make(chan struct{})
			writesDone := make(chan struct{})
			go func() {
				defer close(writesDone)
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						if _, err := c.Write([]byte("x")); err != nil {
							return
						}
					case <-stopWrites:
						return
					}
				}
			}()
			select {
			case <-stalled:
			case <-time.After(time.Second):
				t.Fatal("stall was not detected")
			}
			close(stopWrites)
			c.Close()
			<-writesDone
			limit := firstResponseTimeout
			if scenario == "partial" {
				limit = responseStallTimeout
			}
			if elapsed := time.Since(start); elapsed < limit || elapsed > limit+200*time.Millisecond {
				t.Fatalf("deadline %v, want %v", elapsed, limit)
			}
			if scenario == "partial" && successes.Load() != 1 {
				t.Fatal("missing first-byte success")
			}
		})
	}
}

func TestStallBackoffPerNetwork(t *testing.T) {
	now := time.Unix(1000, 0)
	p := &routePool{network: "a", now: func() time.Time { return now }, clients: []soarClient{&poolFixture{}, &poolFixture{}}, ipv6: []bool{false, false}}
	c := &poolConn{pool: p, index: 0, network: "a"}
	for _, delay := range []time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		c.feedback(true)
		if got := p.stalls["a"][0].Until.Sub(now); got != delay {
			t.Fatalf("backoff %v want %v", got, delay)
		}
		if got := p.order(nil); got[0] != 1 {
			t.Fatal("retry did not prefer backup", got)
		}
		now = now.Add(delay)
	}
	p.network = "b"
	if p.order(nil)[0] != 0 {
		t.Fatal("failure leaked to another network")
	}
	p.network = "a"
	c.feedback(false)
	c.feedback(true)
	if p.stalls["a"][0].Until.Sub(now) != 2*time.Minute {
		t.Fatal("success did not reset backoff")
	}
	(&poolConn{pool: p, index: 1, network: "a"}).feedback(true)
	if len(p.order(nil)) != 2 {
		t.Fatal("blocked routes unavailable as last resort")
	}
	states := p.states()
	if states[0].State != routeFailed || states[0].Reason != "stalled" || states[0].Stalls != 5 {
		t.Fatalf("status %+v", states)
	}
}

func TestStalledSOCKSRetryUsesBackup(t *testing.T) {
	shortStalls(t)
	for _, kind := range []string{"direct", "smart", "tunnel"} {
		t.Run(kind, func(t *testing.T) {
			var firstCalls, backupCalls atomic.Int64
			var mu sync.Mutex
			var servers sync.WaitGroup
			var peers []net.Conn
			pair := func() (net.Conn, net.Conn) {
				a, b := net.Pipe()
				mu.Lock()
				peers = append(peers, b)
				mu.Unlock()
				return a, b
			}
			silent := func(context.Context, string, string) (net.Conn, error) {
				firstCalls.Add(1)
				a, b := pair()
				servers.Add(1)
				go func() { defer servers.Done(); io.Copy(io.Discard, b) }()
				return a, nil
			}
			backup := func(context.Context, string, string) (net.Conn, error) {
				backupCalls.Add(1)
				a, b := pair()
				servers.Add(1)
				go func() { defer servers.Done(); defer b.Close(); io.Copy(b, b) }()
				return a, nil
			}
			tt := newTestTunnel(t, ModeAuto, silent, backup)
			if kind == "smart" {
				tt.ways[RouteSmart] = way{silent, time.Second}
				tt.ways[RouteDirect] = way{}
			}
			if kind == "tunnel" {
				p := &routePool{clients: []soarClient{&poolFixture{dial: silent}, &poolFixture{dial: backup}}, ipv6: []bool{false, false}, kinds: []string{"http", "turn"}, ids: []string{"a", "b"}}
				tt.client = p
				tt.ways[RouteTunnel] = way{p.DialContext, time.Second}
				tt.mode = ModeAlways
			}
			defer func() {
				tt.Close()
				mu.Lock()
				defer mu.Unlock()
				for _, p := range peers {
					p.Close()
				}
				servers.Wait()
			}()
			addr := "relay.example.org:993"
			if kind == "smart" {
				tt.mu.Lock()
				tt.relayLocked("relay.example.org").route = RouteSmart
				tt.mu.Unlock()
			}
			c, code := socksConnect(t, tt.Addr(), "u", "p", addr)
			if code != repSucceeded {
				t.Fatal(code)
			}
			c.Write([]byte("x"))
			c.SetReadDeadline(time.Now().Add(time.Second))
			_, err := c.Read(make([]byte, 1))
			c.Close()
			if err == nil {
				t.Fatal("silent connection survived")
			}
			retry, code := socksConnect(t, tt.Addr(), "u", "p", addr)
			if code != repSucceeded {
				t.Fatal(code)
			}
			defer retry.Close()
			retry.SetDeadline(time.Now().Add(time.Second))
			retry.Write([]byte("x"))
			if _, err := io.ReadFull(retry, make([]byte, 1)); err != nil {
				t.Fatal("backup failed", err)
			}
			if firstCalls.Load() != 1 || backupCalls.Load() != 1 {
				t.Fatalf("calls %d/%d", firstCalls.Load(), backupCalls.Load())
			}
			tt.mu.Lock()
			var g *group
			for _, v := range tt.relays {
				g = v
			}
			tt.mu.Unlock()
			if kind != "tunnel" {
				tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true, SmartOK: kind == "smart"})
				tt.mu.Lock()
				r := g.route
				tt.mu.Unlock()
				if r == RouteDirect || kind == "smart" && r == RouteSmart {
					t.Fatal("TLS probe restored stalled route", r)
				}
			}
		})
	}
}

func TestWatchCloseCancelsDeadline(t *testing.T) {
	shortStalls(t)
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer b.Close(); io.Copy(io.Discard, b) }()
	stalled := make(chan struct{})
	c := watchResponses(a, nil, func() { close(stalled) })
	c.Write([]byte("x"))
	c.Close()
	<-done
	select {
	case <-stalled:
		t.Fatal("closed connection stalled")
	case <-time.After(2 * firstResponseTimeout):
	}
}

func TestBlockedPoolLastResort(t *testing.T) {
	var calls atomic.Int64
	dial := func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	p := &routePool{clients: []soarClient{&poolFixture{dial: dial}}, ipv6: []bool{false}}
	(&poolConn{pool: p, index: 0}).feedback(true)
	c, err := p.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if calls.Load() != 1 {
		t.Fatal("last resort was not used")
	}
	// A successful dial alone must not clear a stalled route.
	if p.states()[0].Reason != "stalled" {
		t.Fatal("dial cleared stall backoff")
	}
	poolFeedback(c, false)
	if p.states()[0].Reason != "" {
		t.Fatal("first byte did not clear backoff")
	}
}

func TestStallDecisionHysteresis(t *testing.T) {
	res := probeResult{Stalled: true, SmartOK: true}
	r, f, o, _ := decide(RouteDirect, res, 0, 2, true)
	if r != RouteDirect || f != 1 || o != 0 {
		t.Fatal("first failure bypassed hysteresis")
	}
	r, f, _, _ = decide(r, res, f, o, true)
	if r != RouteSmart || f != 2 {
		t.Fatal("second failure did not select smart")
	}
	r, _, _, _ = decide(r, probeResult{Stalled: true}, f, 0, true)
	if r != RouteTunnel {
		t.Fatal("smart stall did not select tunnel")
	}
}

func TestStallDuringBlockedWrite(t *testing.T) {
	shortStalls(t)
	a, b := net.Pipe()
	served := make(chan struct{})
	release := make(chan struct{})
	go func() {
		defer close(served)
		defer b.Close()
		io.CopyN(io.Discard, b, 1)
		b.Write(bytes.Repeat([]byte("x"), 16*1024))
		<-release
	}()
	stalled := make(chan struct{})
	c := watchResponses(a, nil, func() { close(stalled) })
	written := make(chan struct{})
	go func() { defer close(written); c.Write(make([]byte, 64*1024)) }()
	defer func() { c.Close(); close(release); <-served; <-written }()
	if _, err := io.ReadFull(c, make([]byte, 16*1024)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stalled:
	case <-time.After(time.Second):
		t.Fatal("blocked upload had no stall deadline")
	}
}

func TestPoolDoesNotRaceStalledRoute(t *testing.T) {
	saved := routeStagger
	routeStagger = 5 * time.Millisecond
	defer func() { routeStagger = saved }()
	var blockedCalls atomic.Int64
	blocked := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
		blockedCalls.Add(1)
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}}
	ready := &poolFixture{dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		timer := time.NewTimer(40 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			a, b := net.Pipe()
			b.Close()
			return a, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	p := &routePool{clients: []soarClient{blocked, ready}, ipv6: []bool{false, false}}
	(&poolConn{pool: p, index: 0}).feedback(true)
	c, err := p.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if blockedCalls.Load() != 0 {
		t.Fatal("stalled route raced a usable backup")
	}
}
