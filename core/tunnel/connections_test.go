package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

type shortHandshakeConn struct {
	net.Conn
	first bool
}

func (c *shortHandshakeConn) SetDeadline(deadline time.Time) error {
	if !c.first {
		c.first = true
		deadline = time.Now().Add(30 * time.Millisecond)
	}
	return c.Conn.SetDeadline(deadline)
}

func TestSlowDialOutlivesHandshakeDeadline(t *testing.T) {
	var calls atomic.Int32
	echo := echoDialer("ok:", &calls)
	tt := newTestTunnel(t, ModeAlways, nil, func(ctx context.Context, n, a string) (net.Conn, error) {
		time.Sleep(60 * time.Millisecond)
		return echo(ctx, n, a)
	})
	server, client := net.Pipe()
	defer client.Close()
	client.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan struct{})
	go func() { tt.handle(&shortHandshakeConn{Conn: server}); close(done) }()
	if err := socks(client, "example.org:443", links.SocksAuth{Username: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	if got := roundTrip(t, client); got != "ok:hi" {
		t.Fatalf("response = %q", got)
	}
	<-done
}

func waitClients(t *testing.T, tt *Tunnel, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.mu.Lock()
		n := len(tt.clients)
		tt.mu.Unlock()
		if n == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("client count did not reach %d", count)
}

func TestPendingHandshakeLimitAndShutdown(t *testing.T) {
	tt := newTestTunnel(t, ModeAuto, nil, nil)
	for range maxPendingHandshakes {
		c, err := net.Dial("tcp", tt.Addr())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	waitClients(t, tt, maxPendingHandshakes)
	extra, err := net.Dial("tcp", tt.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = extra.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("excess client: %v", err)
	}
	tt.Close()
	waitClients(t, tt, 0)
}

func TestConnectionLimit(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { io.Copy(io.Discard, b); b.Close() }()
		return a, nil
	}
	tt := newTestTunnel(t, ModeAuto, dial, nil)
	for range maxConnections {
		c, code := socksConnect(t, tt.Addr(), "u", "p", "example.org:443")
		if code != repSucceeded {
			t.Fatalf("reply = %d", code)
		}
		defer c.Close()
	}
	waitClients(t, tt, maxConnections)
	extra, err := net.Dial("tcp", tt.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = extra.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("excess client: %v", err)
	}
}

func TestStopCancelsPendingDial(t *testing.T) {
	started := make(chan struct{})
	tt := newTestTunnel(t, ModeAuto, func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	c, err := net.Dial("tcp", tt.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- socks(c, "example.org:443", links.SocksAuth{Username: "u", Password: "p"}) }()
	<-started
	tt.Close()
	if err := <-done; err == nil {
		t.Fatal("dial succeeded after stop")
	}
	waitClients(t, tt, 0)
}

func TestSocksDestinationRefused(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	client.SetDeadline(time.Now().Add(time.Second))
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		if _, err := socksHandshake(server, "u", "p"); err != nil {
			done <- err
			return
		}
		_, err := server.Write([]byte{5, repHostUnreachable, 0, 1})
		done <- err
	}()
	if err := socks(client, "example.org:443", links.SocksAuth{Username: "u", Password: "p"}); !errors.Is(err, errDestinationRefused) {
		t.Fatalf("destination refusal = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
