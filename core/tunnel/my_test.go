package tunnel

import (
	"context"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

func TestParseMy(t *testing.T) {
	for _, tc := range []struct {
		in, addr, user, pass string
		ok                   bool
	}{
		{"127.0.0.1:9050", "127.0.0.1:9050", "", "", true},
		{"socks5://proxy.example.org:1080", "proxy.example.org:1080", "", "", true},
		{"socks5h://u:p@[::1]:1080", "[::1]:1080", "u", "p", true},
		{"u:p@10.0.0.2:1080", "10.0.0.2:1080", "u", "p", true},
		{"127.0.0.1", "", "", "", false},
		{"127.0.0.1:0", "", "", "", false},
		{"http://127.0.0.1:8080", "", "", "", false},
		{"127.0.0.1:9050/x", "", "", "", false},
		{":9050", "", "", "", false},
	} {
		addr, auth, err := parseMy(tc.in)
		if (err == nil) != tc.ok || tc.ok && (addr != tc.addr || auth.Username != tc.user || auth.Password != tc.pass) {
			t.Errorf("parseMy(%q) = %q %+v %v", tc.in, addr, auth, err)
		}
	}
}

func TestMyRouteOrder(t *testing.T) {
	route := func(id string, priority uint16) links.Route {
		return links.Route{ID: id, Type: "iroh", Priority: priority}
	}
	for _, tc := range []struct {
		routes []links.Route
		want   []string
	}{
		{nil, []string{"my"}},
		{[]links.Route{route("b", 20), route("a", 10)}, []string{"my", "a", "b"}},
		{[]links.Route{route("z", 5), route("a", 10)}, []string{"my", "z", "a"}}, // ties go to My
		{[]links.Route{route("first", 3), route("a", 10)}, []string{"first", "my", "a"}},
	} {
		c, err := newRoutePool(Config{Routes: tc.routes, My: "127.0.0.1:9050"})
		if err != nil {
			t.Fatal(err)
		}
		p := c.(*routePool)
		if !slices.Equal(p.ids, tc.want) || p.kinds[slices.Index(p.ids, "my")] != "my" {
			t.Errorf("order %v, want %v", p.ids, tc.want)
		}
		p.Close()
	}
}

func TestMyRouteRejectsLoop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if _, err := Start(Config{My: addr, Listen: addr}); err == nil || !strings.Contains(err.Error(), "points at this proxy") {
		t.Fatalf("Start = %v", err)
	}
}

// fakeUpstream is a SOCKS5 proxy that sends every CONNECT to echo and
// records the requested hosts.
func fakeUpstream(t *testing.T, user, pass, echo string) (string, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var hosts []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				head := make([]byte, 3)
				io.ReadFull(c, head)
				if user == "" {
					c.Write([]byte{5, 0})
				} else {
					c.Write([]byte{5, 2})
					b := make([]byte, 2)
					io.ReadFull(c, b)
					u := make([]byte, b[1])
					io.ReadFull(c, u)
					io.ReadFull(c, b[:1])
					p := make([]byte, b[0])
					io.ReadFull(c, p)
					if string(u) != user || string(p) != pass {
						c.Write([]byte{1, 1})
						return
					}
					c.Write([]byte{1, 0})
				}
				req := make([]byte, 5)
				io.ReadFull(c, req)
				host := make([]byte, req[4])
				io.ReadFull(c, host)
				io.ReadFull(c, req[:2])
				mu.Lock()
				hosts = append(hosts, string(host))
				mu.Unlock()
				up, err := net.Dial("tcp", echo)
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go io.Copy(up, c)
				io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hosts...)
	}
}

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b := make([]byte, 64)
				n, _ := c.Read(b)
				c.Write(append([]byte("echo:"), b[:n]...))
			}()
		}
	}()
	return ln.Addr().String()
}

func TestMyRouteCarriesConnections(t *testing.T) {
	echo := echoServer(t)
	for _, auth := range []string{"", "u:p@"} {
		user, pass := "", ""
		if auth != "" {
			user, pass = "u", "p"
		}
		upstream, hosts := fakeUpstream(t, user, pass, echo)
		tun, err := Start(Config{My: auth + upstream, Mode: "my", Listen: "127.0.0.1:0", User: "a", Pass: "b"})
		if err != nil {
			t.Fatal(err)
		}
		c, rep := socksConnect(t, tun.Addr(), "a", "b", "chat.example.org:443")
		if rep != 0 {
			t.Fatalf("auth %q: reply %d", auth, rep)
		}
		if got := roundTrip(t, c); got != "echo:hi" {
			t.Errorf("auth %q: got %q", auth, got)
		}
		c.Close()
		if h := hosts(); !slices.Contains(h, "chat.example.org") {
			t.Errorf("auth %q: upstream saw %v, want the hostname unresolved", auth, h)
		}
		tun.Close()
	}
}

func TestMyRouteDownFallsThroughAtOnce(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	my, _ := newMyClient(closed)
	a, b := net.Pipe()
	defer b.Close()
	link := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return a, nil }}
	pool := &routePool{clients: []soarClient{my, link}, ipv6: []bool{true, false}, kinds: []string{"my", "wss"}, ids: []string{"my", "w"}}
	start := time.Now()
	conn, err := pool.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil || connVia(conn) != "wss" {
		t.Fatalf("dial = %v via %q", err, connVia(conn))
	}
	if d := time.Since(start); d >= routeStagger {
		t.Errorf("took %v, want the next route without waiting", d)
	}
	if pool.states()[0].State != routeFailed {
		t.Errorf("my route state %q, want failed", pool.states()[0].State)
	}
	conn.Close()
}
