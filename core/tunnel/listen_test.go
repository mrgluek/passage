package tunnel

import (
	"net"
	"testing"
	"time"
)

func TestTunnelRestartSamePort(t *testing.T) {
	first, err := Start(Config{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	address := first.Addr()
	first.Close()
	for range 5 {
		next, err := Start(Config{Listen: address})
		if err != nil {
			t.Fatal(err)
		}
		next.Close()
	}
}

func TestListenProxyRetry(t *testing.T) {
	for _, release := range []bool{true, false} {
		t.Run(map[bool]string{true: "released", false: "busy"}[release], func(t *testing.T) {
			busy, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer busy.Close()
			if release {
				time.AfterFunc(50*time.Millisecond, func() { busy.Close() })
			}
			ln, retried, err := listenProxy(busy.Addr().String(), 250*time.Millisecond)
			if ln != nil {
				defer ln.Close()
			}
			if !retried || (err == nil) != release {
				t.Fatalf("retried=%t err=%v", retried, err)
			}
		})
	}
}
