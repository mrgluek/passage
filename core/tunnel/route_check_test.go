package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

func stageNames(stages []RouteStage) string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.Stage
		if !s.OK {
			names[i] += "!"
		}
	}
	return strings.Join(names, " ")
}

func tlsFixture(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	probeRoots = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	t.Cleanup(func() { probeRoots = nil })
	return srv
}

func TestCheckServerStages(t *testing.T) {
	srv := tlsFixture(t, func(http.ResponseWriter, *http.Request) {})
	addr := srv.Listener.Addr().String()
	ok := func(context.Context) error { return nil }
	bad := func(context.Context) error { return errors.New("refused") }

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closed.Addr().String()
	closed.Close()

	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	go func() {
		for {
			c, err := plain.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	tests := []struct {
		name   string
		server routeServer
		want   string
	}{
		{"all", routeServer{addr: addr, tls: true, app: ok}, "tcp tls app"},
		{"app refused", routeServer{addr: addr, tls: true, app: bad}, "tcp tls app!"},
		{"no tls", routeServer{addr: addr}, "tcp"},
		{"tcp refused", routeServer{addr: closedAddr, tls: true, app: ok}, "tcp!"},
		{"tls closed", routeServer{addr: plain.Addr().String(), tls: true, app: ok}, "tcp tls!"},
		{"no server", routeServer{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stageNames(checkServer(context.Background(), tt.server)); got != tt.want {
				t.Fatalf("stages %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckServerNamesNoServer(t *testing.T) {
	c := checkRoute(context.Background(), "wss", routeServer{addr: "127.0.0.1:1"}, nil, nil)
	for _, s := range c.Stages {
		if strings.Contains(s.Error, "127.0.0.1") {
			t.Fatalf("stage error names the server: %q", s.Error)
		}
	}
}

func dialTo(addr string) dialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, network, addr)
	}
}

func TestProbeDataVia(t *testing.T) {
	page := strings.Repeat("x", 1000)
	srv := tlsFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(page))
	})
	dial := dialTo(srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	n, err := probeDataVia(ctx, dial, "example.com:443")
	if err != nil || n < 1000 {
		t.Fatalf("page: %d bytes, %v", n, err)
	}
	if n, err = probeDataVia(ctx, dial, "example.com:993"); err != nil || n == 0 {
		t.Fatalf("handshake only: %d bytes, %v", n, err)
	}
}

func TestProbeDataViaKeepsBytesOnFailure(t *testing.T) {
	srv := tlsFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.Write([]byte(strings.Repeat("x", 1000)))
		w.(http.Flusher).Flush()
		// Stall: the probe should stop at its deadline with what it has.
		time.Sleep(time.Second)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	n, err := probeDataVia(ctx, dialTo(srv.Listener.Addr().String()), "example.com:443")
	if !errors.Is(err, context.DeadlineExceeded) || n == 0 {
		t.Fatalf("got %d bytes, %v", n, err)
	}
}

func TestDataTargets(t *testing.T) {
	rows := []DiagRow{{Target: "tg.example.org:443", Kind: "telegram"}, {Target: "a.example.org:993", Kind: "relay"}, {Target: "b.example.org:443", Kind: "canary"}}
	want := []dataTarget{{"b.example.org:443", false}, {"a.example.org:993", false}, {"tg.example.org:443", true}}
	if got := dataTargets(rows); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestCheckRouteSkipsRefusedTargets(t *testing.T) {
	srv := tlsFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("hello")) })
	real := dialTo(srv.Listener.Addr().String())
	var tried []string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		tried = append(tried, addr)
		if addr == "no.example.org:443" {
			return nil, errDestinationRefused
		}
		return real(ctx, network, addr)
	}
	c := checkRoute(context.Background(), "wss", routeServer{}, dial, []dataTarget{{addr: "no.example.org:443"}, {addr: "example.com:443"}})
	if len(tried) != 2 || stageNames(c.Stages) != "data" || c.Stages[0].Bytes == 0 {
		t.Fatalf("tried %v, stages %+v", tried, c.Stages)
	}
	if s := c.Stages[0]; s.Target != "example.com:443" || s.Refused != 1 {
		t.Fatalf("stage %+v", s)
	}
}

func TestCheckRouteNamesFailedTarget(t *testing.T) {
	dial := func(ctx context.Context, _, addr string) (net.Conn, error) {
		if addr == "no.example.org:443" {
			return nil, errDestinationRefused
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c := checkRoute(ctx, "wss", routeServer{}, dial, []dataTarget{{addr: "no.example.org:443"}, {addr: "hang.example.org:443"}, {addr: "never.example.org:443"}})
	s := c.Stages[0]
	if s.OK || s.Error != "timeout" || s.Target != "hang.example.org:443" || s.Refused != 1 {
		t.Fatalf("stage %+v", s)
	}
}

func TestCheckRouteAllRefused(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused }
	c := checkRoute(context.Background(), "wss", routeServer{}, dial, []dataTarget{{addr: "a.example.org:443"}, {addr: "b.example.org:443"}})
	if s := c.Stages[0]; s.OK || s.Target != "b.example.org:443" || s.Refused != 1 {
		t.Fatalf("stage %+v", s)
	}
}

func TestCheckRouteRetriesStallOnFreshSession(t *testing.T) {
	srv := tlsFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("hello")) })
	hang := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	closed := false
	server := routeServer{
		fresh: func() (dialFunc, func(), error) {
			return dialTo(srv.Listener.Addr().String()), func() { closed = true }, nil
		},
		received: func() int64 { return 5000 },
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkDataRetryTimeout+3*time.Second)
	defer cancel()
	c := checkRoute(ctx, "wss", server, hang, []dataTarget{{addr: "example.com:443"}})
	if stageNames(c.Stages) != "data! fresh" || c.Stages[0].Received != 5000 || !closed {
		t.Fatalf("stages %+v, closed %v", c.Stages, closed)
	}
}

func TestCheckRouteAllRefusedSkipsFresh(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused }
	server := routeServer{fresh: func() (dialFunc, func(), error) {
		t.Fatal("fresh session after a refusal")
		return nil, nil, nil
	}}
	if got := stageNames(checkRoute(context.Background(), "wss", server, dial, []dataTarget{{addr: "a.example.org:443"}}).Stages); got != "data!" {
		t.Fatalf("stages %q", got)
	}
}

func TestWSSClientCountsReceived(t *testing.T) {
	client, exit := wssFixture(t, 6, func(c net.Conn) { io.Copy(io.Discard, c) })
	defer client.Close()
	transport := exit.Client().Transport.(*http.Transport)
	transport.DialContext = countingDial(new(net.Dialer).DialContext)
	conn, err := client.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if client.received() == 0 {
		t.Fatal("no bytes counted")
	}
}

func TestURLServer(t *testing.T) {
	for raw, want := range map[string]string{
		"wss://example.org/path":      "example.org:443",
		"wss://example.org:8443/path": "example.org:8443",
		"https://example.org":         "example.org:443",
	} {
		if s := urlServer(raw); s.addr != want || !s.tls {
			t.Fatalf("%s: got %+v", raw, s)
		}
	}
}

func TestHTTPServerUsesDialIP(t *testing.T) {
	client, err := NewHTTPClient(links.HTTPParams{Endpoint: "https://example.org/passage", Key: wssTestKey, DialIP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	s := httpServer(links.HTTPParams{Endpoint: "https://example.org/passage", Key: wssTestKey, DialIP: "192.0.2.1"}, client)
	if s.addr != "192.0.2.1:443" || s.name != "example.org" || !s.tls || s.app == nil || s.fresh == nil {
		t.Fatalf("got %+v", s)
	}
}
