package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func freeForwardAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func localEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); defer c.Close(); io.Copy(c, c) }()
		}
	}()
	t.Cleanup(func() { ln.Close(); wg.Wait() })
	return ln.Addr().String()
}

func forwardConnect(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	t.Cleanup(func() { c.Close() })
	return c
}

func TestForwardEchoAndClose(t *testing.T) {
	target, addr := localEcho(t), freeForwardAddr(t)
	tt, err := Start(Config{Mode: ModeOff, User: "u", Pass: "p", Forwards: []Forward{{Name: "test", Listen: addr, Target: target}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tt.Close)
	initial := tt.Status(10).Forwards[0]
	if !initial.Up || initial.Conns != 0 || initial.LastConn != nil || initial.Error != "" {
		t.Fatalf("initial status: %+v", initial)
	}
	clients := []net.Conn{forwardConnect(t, addr), forwardConnect(t, addr)}
	payload := bytes.Repeat([]byte("forward stream\x00\xff"), 200000)
	errs := make(chan error, len(clients))
	for _, c := range clients {
		go func() {
			written := make(chan error, 1)
			go func() { _, err := io.Copy(c, bytes.NewReader(payload)); written <- err }()
			got := make([]byte, len(payload))
			_, err := io.ReadFull(c, got)
			if writeErr := <-written; err == nil {
				err = writeErr
			}
			if err == nil && !bytes.Equal(got, payload) {
				err = fmt.Errorf("payload changed")
			}
			errs <- err
		}()
	}
	for range clients {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	s := tt.Status(20)
	if f := s.Forwards[0]; !f.Up || f.Conns != 2 || f.LastConn == nil || f.LastConn.Before(tt.started) {
		t.Fatalf("connected status: %+v", f)
	}
	if s.Stats["direct"].Conns != 2 || s.Active["direct"] != 2 {
		t.Fatalf("shared counters: %+v %+v", s.Stats, s.Active)
	}
	*s.Forwards[0].LastConn = time.Time{}
	if tt.Status(0).Forwards[0].LastConn.IsZero() {
		t.Fatal("snapshot aliases tunnel state")
	}
	tt.Close()
	waitClients(t, tt, 0)
	if tt.Status(0).Forwards[0].Up {
		t.Fatal("listener up after close")
	}
	for _, c := range clients {
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("connection survived close")
		}
	}
	for _, address := range []string{tt.Addr(), addr} {
		ln, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("port not freed: %v", err)
		}
		ln.Close()
	}
	count := 0
	for _, e := range s.Events {
		if strings.HasPrefix(e.Msg, "forward ") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("forward history entries = %d", count)
	}
}

func TestForwardBindFailureKeepsSocks(t *testing.T) {
	target := localEcho(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	tt, err := Start(Config{Mode: ModeOff, User: "u", Pass: "p", Forwards: []Forward{
		{Listen: busy.Addr().String(), Target: target}, {Listen: freeForwardAddr(t), Target: target},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer tt.Close()
	s := tt.Status(20)
	if s.Forwards[0].Up || s.Forwards[0].Error == "" || !s.Forwards[1].Up {
		t.Fatalf("bind status: %+v", s.Forwards)
	}
	c, code := socksConnect(t, tt.Addr(), "u", "p", target)
	defer c.Close()
	if code != repSucceeded {
		t.Fatalf("SOCKS reply = %d", code)
	}
	if _, err := c.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 2)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ok" {
		t.Fatalf("SOCKS echo = %q, %v", got, err)
	}
	count := 0
	for _, e := range s.Events {
		if strings.HasPrefix(e.Msg, "forward ") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("forward history entries = %d", count)
	}
}

func TestForwardValidation(t *testing.T) {
	valid := Forward{Listen: "127.0.0.1:18001", Target: "example.org:443"}
	for _, tc := range []struct{ name, listen, target, want string }{
		{"wildcard", ":18001", "", "listen must"},
		{"external", "192.0.2.1:18001", "", "listen must"},
		{"hostname listen", "localhost:18001", "", "listen must"},
		{"IPv6 listen", "[::1]:18001", "", "listen must"},
		{"missing listen port", "127.0.0.1", "", "listen must"},
		{"zero listen port", "127.0.0.1:0", "", "listen must"},
		{"large listen port", "127.0.0.1:65536", "", "listen must"},
		{"missing host", "", ":443", "target must"},
		{"missing port", "", "example.org", "target must"},
		{"bad host", "", "bad host:443", "target must"},
		{"credentials", "", "user@example.org:443", "target must"},
		{"service port", "", "example.org:https", "target must"},
		{"zero target port", "", "example.org:0", "target must"},
		{"large target port", "", "example.org:65536", "target must"},
		{"duplicate", "", "", "duplicate"},
		{"duplicate spelling", "127.0.0.1:018001", "", "duplicate"},
		{"SOCKS collision", "", "", "SOCKS listener"},
		{"too many", "", "", "at most 8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Forwards: []Forward{valid}}
			if tc.listen != "" {
				cfg.Forwards[0].Listen = tc.listen
			}
			if tc.target != "" {
				cfg.Forwards[0].Target = tc.target
			}
			switch tc.name {
			case "duplicate", "duplicate spelling":
				cfg.Forwards = append(cfg.Forwards, valid)
			case "SOCKS collision":
				cfg.Listen = valid.Listen
			case "too many":
				cfg.Forwards = make([]Forward, 9)
			}
			tt, err := Start(cfg)
			if tt != nil {
				tt.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	for _, target := range []string{"example.org:443", "relay.example.org.:1", "127.0.0.1:65535", "[::1]:443"} {
		if err := validateForwards(Config{Forwards: []Forward{{Listen: valid.Listen, Target: target}}}); err != nil {
			t.Fatalf("valid target %q: %v", target, err)
		}
	}
}

func TestForwardConfigJSON(t *testing.T) {
	for _, raw := range []string{`{}`, `{"unknown":[]}`, `{"forwards":[{"name":"test","listen":"127.0.0.1:18001","target":"example.org:443","unknown":true}]}`} {
		var cfg Config
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "forwards") {
			if len(cfg.Forwards) != 1 || cfg.Forwards[0].Name != "test" || cfg.Forwards[0].Target != "example.org:443" {
				t.Fatalf("JSON config: %+v", cfg.Forwards)
			}
		} else if len(cfg.Forwards) != 0 {
			t.Fatal("unexpected forwards")
		}
	}
}

func TestForwardUsesRouter(t *testing.T) {
	for _, mode := range []string{ModeAuto, "route:test"} {
		t.Run(mode, func(t *testing.T) {
			var direct, first, selected atomic.Int32
			pool := &routePool{
				clients: []soarClient{&poolFixture{dial: echoDialer("A:", &first)}, &poolFixture{dial: echoDialer("T:", &selected)}},
				ipv6:    []bool{false, false}, kinds: []string{"http", "http"}, ids: []string{"first", "test"},
			}
			tt := newTestTunnel(t, ModeAuto, failDialer(&direct), pool.DialContext)
			tt.client = pool
			if err := tt.SetMode(mode); err != nil {
				t.Fatal(err)
			}
			addr := freeForwardAddr(t)
			tt.cfg.Forwards = []Forward{{Listen: addr, Target: "example.org:443"}}
			tt.startForwards()
			want := "A:hi"
			if mode != ModeAuto {
				want = "T:hi"
			}
			c := forwardConnect(t, addr)
			if got := roundTrip(t, c); got != want {
				t.Fatalf("forward response = %q, want %q", got, want)
			}
			for _, e := range tt.Status(20).Events {
				if strings.Contains(e.Msg, " dial ") {
					t.Fatalf("per-connection forward history: %s", e.Msg)
				}
			}
			socks, code := socksConnect(t, tt.Addr(), "u", "p", "example.org:443")
			defer socks.Close()
			if code != repSucceeded || roundTrip(t, socks) != want {
				t.Fatal("SOCKS and forward chose different routes")
			}
			if mode == ModeAuto {
				if direct.Load() != 2 || first.Load() != 2 || selected.Load() != 0 {
					t.Fatalf("auto dials: %d %d %d", direct.Load(), first.Load(), selected.Load())
				}
			} else if direct.Load() != 0 || first.Load() != 0 || selected.Load() != 2 {
				t.Fatalf("pinned dials: %d %d %d", direct.Load(), first.Load(), selected.Load())
			}
			s := tt.Status(0)
			if s.Groups["example.org"].Kind != "relay" || s.Stats["tunnel"].Conns != 2 {
				t.Fatalf("shared router status: %+v", s)
			}
		})
	}
}

func TestForwardCloseCancelsDial(t *testing.T) {
	started := make(chan struct{})
	tt := newTestTunnel(t, ModeOff, func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	addr := freeForwardAddr(t)
	tt.cfg.Forwards = []Forward{{Listen: addr, Target: "example.org:443"}}
	tt.startForwards()
	forwardConnect(t, addr)
	<-started
	done := make(chan struct{})
	go func() { tt.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("close left dial or accept goroutines running")
	}
	waitClients(t, tt, 0)
}

func TestForwardConnectionCap(t *testing.T) {
	target, addr := localEcho(t), freeForwardAddr(t)
	tt, err := Start(Config{Mode: ModeOff, Forwards: []Forward{{Name: "cap", Listen: addr, Target: target}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tt.Close)
	var open []net.Conn
	for range maxForwardConnections {
		c := forwardConnect(t, addr)
		c.Write([]byte("x"))
		buf := make([]byte, 1)
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Fatal(err)
		}
		open = append(open, c)
	}
	extra := forwardConnect(t, addr)
	extra.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := extra.Read(make([]byte, 1)); err == nil {
		t.Fatal("a connection past the forward cap was served")
	}
	// The SOCKS listener is unaffected.
	socks, err := net.DialTimeout("tcp", tt.Addr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	socks.Close()
	for _, c := range open {
		c.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		tt.mu.Lock()
		n := tt.forwardConns
		tt.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("forward counter stuck at %d", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
