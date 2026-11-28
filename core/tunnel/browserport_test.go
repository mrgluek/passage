package tunnel

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A browser that connects while one router stops and the next starts is
// served by the next one, not refused.
func TestBrowserPortOutlivesARouterRestart(t *testing.T) {
	port, err := ListenBrowserPort("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer port.Close()
	start := func() *Tunnel {
		tt := newTestTunnel(t, ModeAuto, echoDialer("d:", new(atomic.Int32)), nil)
		tt.cfg.Sites = testSites
		tt.cfg.BrowserPort = port
		tt.siteDNS = fakeSiteDNS(nil, nil)
		tt.startBrowser()
		return tt
	}
	first := start()
	first.Close()
	// Between the two routers: the port still accepts.
	c, err := net.DialTimeout("tcp", port.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("refused during the restart: %v", err)
	}
	defer c.Close()
	second := start()
	defer second.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err := c.Read(reply); err != nil || reply[0] != 5 {
		t.Fatalf("the next router did not answer: %v %v", reply, err)
	}
}

type failingListener struct {
	calls  atomic.Int64
	closed chan struct{}
	once   sync.Once
}

func (l *failingListener) Accept() (net.Conn, error) {
	l.calls.Add(1)
	return nil, errors.New("accept failed")
}
func (l *failingListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *failingListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func TestBrowserPortAcceptPausesOnErrors(t *testing.T) {
	ln := &failingListener{closed: make(chan struct{})}
	p := &BrowserPort{ln: ln, conns: make(chan net.Conn), done: make(chan struct{})}
	finished := make(chan struct{})
	go func() { p.accept(); close(finished) }()
	time.Sleep(300 * time.Millisecond)
	if n := ln.calls.Load(); n > 12 {
		t.Fatalf("%d Accept calls in 300 ms: no pause between failures", n)
	}
	p.Close()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("accept did not stop after Close")
	}
}
