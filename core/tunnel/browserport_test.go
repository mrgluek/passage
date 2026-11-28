package tunnel

import (
	"net"
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
