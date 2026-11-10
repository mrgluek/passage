package tunnel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func shortStalls(t *testing.T) {
	t.Helper()
	setStalls(t, 40*time.Millisecond, 90*time.Millisecond)
}

// setStalls overrides the stall limits for one test. Tests whose steps are
// shorter than a scheduling hiccup on a loaded CI runner need larger limits.
func setStalls(t *testing.T, first, later time.Duration) {
	t.Helper()
	a, b := firstResponseTimeout, responseStallTimeout
	firstResponseTimeout, responseStallTimeout = first, later
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
			c := watchResponses(a, func() { successes.Add(1) }, func(bool) { close(stalled) })
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
			select {
			case <-stalled:
			case <-time.After(time.Second):
				t.Fatal("stall was not detected")
			}
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

var stallTestID atomic.Uint64

func poolStall(t *testing.T, p *routePool, index int, network string) {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	(&poolConn{Conn: a, pool: p, index: index, network: network, address: fmt.Sprintf("%d.example.org:443", stallTestID.Add(1))}).feedback(true)
}

func TestStallBackoffPerNetwork(t *testing.T) {
	now := time.Unix(1000, 0)
	p := &routePool{network: "a", now: func() time.Time { return now }, clients: []soarClient{&poolFixture{}, &poolFixture{}}, ipv6: []bool{false, false}}
	c := &poolConn{pool: p, index: 0, network: "a"}
	for _, delay := range []time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		poolStall(t, p, 0, "a")
		poolStall(t, p, 0, "a")
		if got := p.stalls["a"][0].until.Sub(now); got != delay {
			t.Fatalf("backoff %v want %v", got, delay)
		}
		if got := p.order(nil); got[0] != 1 {
			t.Fatal("retry did not prefer backup", got)
		}
		now = now.Add(delay + time.Nanosecond)
	}
	p.network = "b"
	if p.order(nil)[0] != 0 {
		t.Fatal("failure leaked to another network")
	}
	p.network = "a"
	c.feedback(false)
	poolStall(t, p, 0, "a")
	poolStall(t, p, 0, "a")
	if p.stalls["a"][0].until.Sub(now) != 2*time.Minute {
		t.Fatal("success did not reset backoff")
	}
	poolStall(t, p, 1, "a")
	poolStall(t, p, 1, "a")
	if len(p.order(nil)) != 2 {
		t.Fatal("blocked routes unavailable as last resort")
	}
	states := p.states()
	if states[0].State != routeFailed || states[0].Reason != "stalled" || states[0].Stalls != 10 {
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
				p := &routePool{log: tt.log, clients: []soarClient{&poolFixture{dial: silent}, &poolFixture{dial: backup}}, ipv6: []bool{false, false}, kinds: []string{"http", "turn"}, ids: []string{"a", "b"}}
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
			var silentConns []net.Conn
			for i := range 2 {
				target := addr
				if kind == "tunnel" && i == 1 {
					target = "relay.example.org:465"
				}
				c, code := socksConnect(t, tt.Addr(), "u", "p", target)
				if code != repSucceeded {
					t.Fatal(code)
				}
				defer c.Close()
				silentConns = append(silentConns, c)
			}
			for _, c := range silentConns {
				c.Write([]byte("x"))
				c.SetReadDeadline(time.Now().Add(time.Second))
				_, err := c.Read(make([]byte, 1))
				c.Close()
				if err == nil {
					t.Fatal("silent connection survived")
				}
			}
			tt.mu.Lock()
			routeName := tt.relays["relay.example.org"].historyUnit() + " " + kind
			tt.mu.Unlock()
			if kind == "tunnel" {
				routeName = "a"
			}
			reason := "first reply"
			if kind == "tunnel" {
				reason = "first reply, 2 destinations"
			}
			expected := "stalled: " + routeName + " (" + reason + "), paused for 2 min, count=2"
			found := 0
			for _, event := range tt.Status(500).Events {
				if strings.HasPrefix(event.Msg, "stalled:") {
					found++
					if event.Msg != expected {
						t.Fatalf("stall event %q, want %q", event.Msg, expected)
					}
				}
			}
			if found != 1 {
				t.Fatalf("got %d stall events", found)
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
			if firstCalls.Load() != 2 || backupCalls.Load() != 1 {
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
	c := watchResponses(a, nil, func(bool) { close(stalled) })
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
	poolStall(t, p, 0, "")
	poolStall(t, p, 0, "")
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
	poolStall(t, p, 0, "")
	poolStall(t, p, 0, "")
	c, err := p.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if blockedCalls.Load() != 0 {
		t.Fatal("stalled route raced a usable backup")
	}
}

func TestUploadProgressRestartsDeadline(t *testing.T) {
	// Pauses between writes are a quarter of the limit, so the limits must be
	// far above the scheduler's jitter on a busy runner.
	setStalls(t, 300*time.Millisecond, 600*time.Millisecond)
	const writes = 8
	for _, received := range []bool{false, true} {
		for _, reply := range []bool{false, true} {
			name := "first-response"
			if received {
				name = "later-response"
			}
			if reply {
				name += "/reply"
			} else {
				name += "/silent"
			}
			t.Run(name, func(t *testing.T) {
				a, b := net.Pipe()
				done := make(chan struct{})
				go func() {
					defer close(done)
					defer b.Close()
					if received {
						b.Write([]byte("hello"))
					}
					for i := 0; i < writes; i++ {
						if _, err := io.CopyN(io.Discard, b, 1); err != nil {
							return
						}
					}
					if reply {
						b.Write([]byte("reply"))
					}
					io.Copy(io.Discard, b)
				}()
				stalled := make(chan struct{})
				c := watchResponses(a, nil, func(bool) { close(stalled) })
				defer func() { c.Close(); b.Close(); <-done }()
				c.SetDeadline(time.Now().Add(15 * time.Second))
				if received {
					if _, err := io.ReadFull(c, make([]byte, 5)); err != nil {
						t.Fatal(err)
					}
				}
				limit := firstResponseTimeout
				if received {
					limit = responseStallTimeout
				}
				var completed time.Time
				for i := 0; i < writes; i++ {
					if _, err := c.Write([]byte("x")); err != nil {
						t.Fatal("upload interrupted", err)
					}
					completed = time.Now()
					select {
					case <-stalled:
						t.Fatal("upload progress counted as a stall")
					case <-time.After(limit / 4):
					}
				}
				if !reply {
					select {
					case <-stalled:
					case <-time.After(2*limit + time.Second):
						t.Fatal("silence after upload did not stall")
					}
					if elapsed := time.Since(completed); elapsed < limit || elapsed > limit+time.Second {
						t.Fatalf("deadline %v, want %v after final write", elapsed, limit)
					}
					return
				}
				if _, err := io.ReadFull(c, make([]byte, 5)); err != nil {
					t.Fatal("reply after upload failed", err)
				}
				select {
				case <-stalled:
					t.Fatal("completed reply counted as a stall")
				case <-time.After(2 * limit):
				}
			})
		}
	}
}

func TestSlowWriteDoesNotStall(t *testing.T) {
	shortStalls(t)
	for _, received := range []bool{false, true} {
		name := "first-response"
		if received {
			name = "later-response"
		}
		t.Run(name, func(t *testing.T) {
			a, b := net.Pipe()
			done := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			allowRead := func() { releaseOnce.Do(func() { close(release) }) }
			go func() {
				defer close(done)
				defer b.Close()
				if received {
					b.Write([]byte("hello"))
				}
				<-release
				if _, err := io.CopyN(io.Discard, b, 1); err != nil {
					return
				}
				b.Write([]byte("reply"))
				io.Copy(io.Discard, b)
			}()
			stalled := make(chan struct{})
			c := watchResponses(a, nil, func(bool) { close(stalled) })
			written := make(chan error, 1)
			defer func() { allowRead(); c.Close(); b.Close(); <-done }()
			c.SetDeadline(time.Now().Add(3 * time.Second))
			if received {
				if _, err := io.ReadFull(c, make([]byte, 5)); err != nil {
					t.Fatal(err)
				}
			}
			go func() { _, err := c.Write([]byte("x")); written <- err }()
			limit := firstResponseTimeout
			if received {
				limit = responseStallTimeout
			}
			select {
			case <-stalled:
				allowRead()
				<-written
				t.Fatal("in-flight write counted as a stall")
			case <-time.After(3 * limit):
			}
			allowRead()
			if err := <-written; err != nil {
				t.Fatal("slow write failed", err)
			}
			if _, err := io.ReadFull(c, make([]byte, 5)); err != nil {
				t.Fatal("prompt reply failed", err)
			}
			select {
			case <-stalled:
				t.Fatal("reply did not clear deadline")
			case <-time.After(2 * limit):
			}
		})
	}
}

func TestStallHistory(t *testing.T) {
	shortStalls(t)
	for _, route := range []string{"direct", "smart", "route-a"} {
		for _, first := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/first=%v", route, first), func(t *testing.T) {
				log := newEventLog(20)
				now := time.Unix(1000, 0)
				state := &hold{}
				feedback := func(stalled, first bool, socket net.Conn) {
					if stalled {
						state.onStall(now, log, route, first, fmt.Sprintf("%p", socket))
					} else {
						state.onReply(log, route)
					}
				}
				force := func() {
					a, b := net.Pipe()
					done := make(chan struct{})
					c := watchResponses(a, nil, func(gotFirst bool) { feedback(true, gotFirst, a); close(done) })
					go func() {
						defer b.Close()
						if !first {
							b.Write([]byte("x"))
						}
						io.Copy(io.Discard, b)
					}()
					if !first {
						if _, err := c.Read(make([]byte, 1)); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := c.Write([]byte("x")); err != nil {
						t.Fatal(err)
					}
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("no stall")
					}
					c.Close()
				}
				force()
				kind := "later"
				if first {
					kind = "first, single"
				}
				want := fmt.Sprintf("%s: connection closed, no reply (%s)", route, kind)
				if events := log.last(20); len(events) != 1 || events[0].Msg != want || state.blocked() {
					t.Fatalf("events %+v, state %+v", events, state)
				}
				if !first {
					force()
					if events := log.last(20); len(events) != 2 || events[1].Msg != want || state.step != 0 {
						t.Fatalf("events %+v, state %+v", events, state)
					}
					return
				}
				force()
				want = fmt.Sprintf("stalled: %s (first reply), paused for 2 min, count=2", route)
				if events := log.last(20); len(events) != 2 || events[1].Msg != want {
					t.Fatalf("events %+v, want %s", events, want)
				}
				force()
				if len(log.last(20)) != 2 {
					t.Fatal("burst was not limited")
				}
				now = now.Add(30 * time.Second)
				force()
				if events := log.last(20); len(events) != 2 || state.step != 1 {
					t.Fatalf("events %+v", events)
				}
				a, b := net.Pipe()
				c := watchResponses(a, func() { feedback(false, true, a) }, func(bool) { t.Error("unexpected stall") })
				go func() { b.Write([]byte("x")); b.Close() }()
				if _, err := c.Read(make([]byte, 1)); err != nil {
					t.Fatal(err)
				}
				c.Close()
				events := log.last(20)
				if len(events) != 3 || events[2].Msg != "resumed: "+route+" (first reply)" || state.step != 0 {
					t.Fatalf("events %+v, state %+v", events, state)
				}
			})
		}
	}
}

func TestPoolStallEvents(t *testing.T) {
	now := time.Unix(1000, 0)
	log := newEventLog(20)
	p := &routePool{log: log, now: func() time.Time { return now }, ids: []string{"route-a"}}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := &poolConn{Conn: a, pool: p, address: "example.org:443"}
	c.feedback(true, false)
	poolStall(t, p, 0, "")
	c.feedback(true, true)
	if events := log.last(20); len(events) != 3 || events[2].Msg != "stalled: route-a (first reply, 2 destinations), paused for 2 min, count=3" {
		t.Fatalf("events %+v", events)
	}
	now = now.Add(10 * time.Minute)
	p.tick(now)
	p.mu.Lock()
	if p.stalledLocked("", 0) {
		t.Error("cooldown did not end")
	}
	p.stalledLocked("", 0)
	p.mu.Unlock()
	if events := log.last(20); len(events) != 4 || events[3].Msg != "resumed: route-a (cooldown ended)" {
		t.Fatalf("events %+v", events)
	}
	c.feedback(false)
	poolStall(t, p, 0, "")
	poolStall(t, p, 0, "")
	if p.stalls[""][0].step != 1 {
		t.Fatal("first reply did not reset step")
	}
}

func TestStallCorroboration(t *testing.T) {
	for _, layer := range []string{"pool", "group", "tunnel"} {
		for _, scenario := range []string{"later", "distinct", "duplicate", "expired", "recovered", "route", "group"} {
			t.Run(layer+"/"+scenario, func(t *testing.T) {
				now := time.Unix(1000, 0)
				log := newEventLog(20)
				p := &routePool{log: log, now: func() time.Time { return now }, ids: []string{"route-a", "route-b"}}
				tt := &Tunnel{log: log, now: func() time.Time { return now }}
				g := newGroup("relay", kindRelay, RouteDirect)
				feedback := func(key string, route int, stalled, first bool) {
					switch layer {
					case "pool":
						(&poolConn{pool: p, network: "a", index: route, address: key}).feedback(stalled, first)
					case "group":
						tt.connectionFeedback(g, Route(route), key, stalled, first)
					case "tunnel":
						tt.connectionFeedback(g, RouteTunnel, key, stalled, first)
					}
				}
				state := func() *hold {
					if layer == "pool" {
						return p.stalls["a"][0]
					}
					if layer == "tunnel" {
						return tt.tunnelStall
					}
					return g.stalls[RouteDirect]
				}
				assertOpen := func() {
					t.Helper()
					s := state()
					if s.blocked() || s.step != 0 {
						t.Fatalf("isolated evidence changed cooldown: %+v", s)
					}
				}
				feedback("a.example.org:443", 0, true, scenario != "later")
				assertOpen()
				switch scenario {
				case "later":
					feedback("b.example.org:443", 0, true, false)
					assertOpen()
					return
				case "duplicate":
					feedback("a.example.org:443", 0, true, true)
					assertOpen()
					return
				case "expired":
					now = now.Add(121 * time.Second)
					feedback("b.example.org:443", 0, true, true)
					assertOpen()
					return
				case "route":
					if layer == "tunnel" {
						return
					}
					feedback("b.example.org:443", 1, true, true)
					assertOpen()
					return
				case "group":
					if layer != "group" {
						return
					}
					other := newGroup("other", kindRelay, RouteDirect)
					tt.connectionFeedback(other, RouteDirect, "b", true, true)
					assertOpen()
					return
				}
				now = now.Add(60 * time.Second)
				feedback("b.example.org:443", 0, true, true)
				s := state()
				if !s.blocked() || s.until != now.Add(2*time.Minute) || s.step != 1 || s.count != 2 {
					t.Fatalf("distinct stalls: %+v", s)
				}
				until := s.until
				feedback("a.example.org:443", 0, true, false)
				if s.until != until || s.count != 3 {
					t.Fatalf("later stall changed hold: %+v", s)
				}
				if scenario == "recovered" {
					feedback("c.example.org:443", 0, false, true)
					assertOpen()
					feedback("a.example.org:443", 0, true, true)
					assertOpen()
				}
			})
		}
	}
}

func TestStallsSwitchGroupOnlyWhenCorroborated(t *testing.T) {
	for _, scenario := range []string{"later", "first", "recovered", "expired", "other"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Unix(1000, 0)
			tt := &Tunnel{log: newEventLog(20), mode: ModeAuto, now: func() time.Time { return now }}
			tt.ways[RouteTunnel].dial = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
			g := newGroup("relay", kindRelay, RouteDirect)
			if scenario == "other" {
				g.kind = kindOther
			}
			tt.connectionFeedback(g, RouteDirect, "1", true, scenario != "later")
			if scenario == "recovered" {
				tt.connectionFeedback(g, RouteDirect, "3", false)
			}
			if scenario == "expired" {
				now = now.Add(121 * time.Second)
			}
			tt.connectionFeedback(g, RouteDirect, "2", true, scenario != "later")
			want := RouteDirect
			if scenario == "first" {
				want = RouteTunnel
			}
			if g.route != want {
				t.Fatalf("route %v, want %v", g.route, want)
			}
			if scenario != "first" && g.failStreak != 0 {
				t.Fatalf("fail streak %d", g.failStreak)
			}
			if tt.stats[RouteDirect].Stalls != 2 {
				t.Fatal(tt.stats[RouteDirect])
			}
		})
	}
}

func TestStallDoesNotCrossGroups(t *testing.T) {
	cfg := Config{Operator: t.Name()}
	tt := &Tunnel{cfg: cfg, log: newEventLog(20), mode: ModeAuto, relays: map[string]*group{}}
	tt.ways[RouteTunnel].dial = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	tt.telegram = newGroup("telegram", kindTelegram, RouteDirect)
	tt.other = newGroup("other", kindOther, RouteDirect)
	relay := newGroup("relay.example.org", kindRelay, RouteDirect)
	tt.relays[relay.name] = relay
	tt.connectionFeedback(tt.telegram, RouteDirect, "1", true)
	tt.connectionFeedback(tt.telegram, RouteDirect, "2", true)
	tt.applyProbe(relay, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true})
	if relay.route != RouteDirect || relay.lastResult.RelayOK != 1 || relay.failStreak != 0 || knownBlocked(cfg, relay.name) {
		t.Fatalf("stall leaked: %+v", relay)
	}
	if tt.routeStatsLocked(RouteDirect).Reason != "stalled" {
		t.Fatal("missing aggregate stall status")
	}
	// A probe cannot release a group's application hold.
	tt.applyProbe(tt.telegram, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true})
	if tt.telegram.route != RouteTunnel || !tt.groupStalledLocked(tt.telegram, RouteDirect) {
		t.Fatal("probe released group")
	}
	// An unrelated group's reply cannot release it either.
	tt.connectionFeedback(relay, RouteDirect, "3", false)
	if !tt.groupStalledLocked(tt.telegram, RouteDirect) {
		t.Fatal("reply crossed groups")
	}
	tt.connectionFeedback(tt.telegram, RouteDirect, "4", false)
	if tt.groupStalledLocked(tt.telegram, RouteDirect) {
		t.Fatal("group reply did not release hold")
	}
}

func TestPinnedRouteNeverCoolsDown(t *testing.T) {
	now := time.Unix(1000, 0)
	failures := 0
	p := &routePool{only: "route:a", kinds: []string{"http", "turn"}, ids: []string{"a", "b"}, clients: []soarClient{&poolFixture{}, &poolFixture{}}, ipv6: []bool{false, false}, log: newEventLog(20), now: func() time.Time { return now }, onFailure: func() { failures++ }}
	for range 4 {
		poolStall(t, p, 0, "")
	}
	s := p.stalls[""][0]
	if s.step != 0 || !s.until.IsZero() || failures != 0 {
		t.Fatalf("pinned cooldown: %+v, callbacks %d", s, failures)
	}
	if got := p.order(nil); len(got) != 1 || got[0] != 0 {
		t.Fatal(got)
	}
	for _, e := range p.log.last(20) {
		if !strings.Contains(e.Msg, "pinned") {
			t.Fatal(e.Msg)
		}
	}
}

func TestRefusedDoesNotMarkTunnelUnhealthy(t *testing.T) {
	tt := newTestTunnel(t, ModeAlways, nil, func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused })
	defer tt.Close()
	c, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:443")
	c.Close()
	if code == repSucceeded {
		t.Fatal("refused destination succeeded")
	}
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if g := tt.relays["relay.example.org"]; g.tunnelFails != 0 {
		t.Fatalf("tunnel failures %d", g.tunnelFails)
	}
}

func TestLegacyTunnelStallCooldown(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(fmt.Sprint(pinned), func(t *testing.T) {
			direct := func(context.Context, string, string) (net.Conn, error) {
				a, b := net.Pipe()
				b.Close()
				return a, nil
			}
			tt := newTestTunnel(t, ModeAuto, direct, direct)
			g := tt.testGroup("relay.example.org:443")
			g.route = RouteTunnel
			if pinned {
				tt.only = "route:legacy-dns"
				tt.mode = ModeAlways
			}
			tt.connectionFeedback(g, RouteTunnel, "a.example.org:443", true)
			tt.connectionFeedback(g, RouteTunnel, "a.example.org:443", true)
			if tt.tunnelStall.step != 0 {
				t.Fatal("same destination cooled down tunnel")
			}
			tt.connectionFeedback(g, RouteTunnel, "b.example.org:443", true)
			c, r, err := tt.dial(g, "relay.example.org:443")
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
			want := RouteTunnel
			if r != want {
				t.Fatalf("dial route %v, want %v", r, want)
			}
			if pinned && tt.tunnelStall.step != 0 {
				t.Fatal("pinned legacy route cooled down")
			}
			tt.connectionFeedback(g, RouteTunnel, "c.example.org:443", false)
			if tt.tunnelStall.step != 0 || !tt.tunnelStall.until.IsZero() {
				t.Fatal("route reply did not clear cooldown")
			}
		})
	}
}

// A held route is deprioritised, never forbidden: with every route held the
// group keeps using its current route, and a failed dial still falls through
// to the other held route.
func TestDialUsesHeldRoutesAsLastResort(t *testing.T) {
	holdRoutes := func(tt *Tunnel, g *group) {
		until := time.Now().Add(time.Hour)
		g.stalls[RouteDirect] = &hold{state: Held, until: until, step: 1}
		tt.tunnelStall = &hold{state: Held, until: until, step: 1}
	}
	for _, current := range []Route{RouteDirect, RouteTunnel} {
		t.Run(current.String(), func(t *testing.T) {
			var directCalls, tunnelCalls atomic.Int32
			tt := newTestTunnel(t, ModeAuto, echoDialer("d:", &directCalls), echoDialer("t:", &tunnelCalls))
			g := tt.testGroup("relay.example.org:443")
			g.route = current
			tt.mu.Lock()
			holdRoutes(tt, g)
			tt.mu.Unlock()
			c, r, err := tt.dial(g, "relay.example.org:443")
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
			if r != current {
				t.Fatalf("all routes held: dial used %v, want the current route %v", r, current)
			}
		})
	}
	t.Run("control: one held route is skipped", func(t *testing.T) {
		var directCalls, tunnelCalls atomic.Int32
		tt := newTestTunnel(t, ModeAuto, echoDialer("d:", &directCalls), echoDialer("t:", &tunnelCalls))
		g := tt.testGroup("relay.example.org:443")
		g.route = RouteDirect
		tt.mu.Lock()
		g.stalls[RouteDirect] = &hold{state: Held, until: time.Now().Add(time.Hour), step: 1}
		tt.mu.Unlock()
		c, r, err := tt.dial(g, "relay.example.org:443")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if r != RouteTunnel {
			t.Fatalf("held direct with a free tunnel: dial used %v", r)
		}
	})
	t.Run("fall through", func(t *testing.T) {
		var directCalls, tunnelCalls atomic.Int32
		tt := newTestTunnel(t, ModeAuto, echoDialer("d:", &directCalls), failDialer(&tunnelCalls))
		g := tt.testGroup("relay.example.org:443")
		g.route = RouteTunnel
		tt.mu.Lock()
		holdRoutes(tt, g)
		tt.mu.Unlock()
		c, r, err := tt.dial(g, "relay.example.org:443")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if r != RouteDirect || tunnelCalls.Load() != 1 || directCalls.Load() != 1 {
			t.Fatalf("route %v, tunnel dials %d, direct dials %d", r, tunnelCalls.Load(), directCalls.Load())
		}
	})
}

func TestStallProbeFlood(t *testing.T) {
	for _, kind := range []groupKind{kindOther, kindRelay} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			tt := newTestTunnel(t, ModeAuto, nil, nil)
			now := time.Now()
			tt.now = func() time.Time { return now }
			g := newGroup("relay.example.org", kind, RouteDirect)
			rounds := 0
			for range 1000 {
				tt.connectionFeedback(g, RouteDirect, "same-connection", true)
				select {
				case <-tt.kick:
					rounds++
				default:
				}
				now = now.Add(50 * time.Millisecond)
			}
			want := 5
			if kind == kindOther {
				want = 0
			}
			if rounds != want || tt.urgent.Load() {
				t.Fatalf("kicks %d, want %d; minimum gap bypass %v", rounds, want, tt.urgent.Load())
			}
		})
	}
}

func TestStallHoldDoesNotEscalateOrRememberBlocked(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	tt := newTestTunnel(t, ModeAuto, nil, func(context.Context, string, string) (net.Conn, error) { return nil, nil })
	tt.cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	g := tt.testGroup("relay.example.org:443")
	for i := range 20 {
		tt.connectionFeedback(g, RouteDirect, fmt.Sprint(i), true)
	}
	if g.stalls[RouteDirect].step != 1 {
		t.Fatal("parallel stalls escalated hold")
	}
	for range blockedAfter + 1 {
		tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true})
	}
	if knownBlocked(tt.cfg, g.name) {
		t.Fatal("hold wrote blocked memory")
	}
}
