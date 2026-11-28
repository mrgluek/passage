package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tlsSite is a TLS web server the probes trust.
func tlsSite(t *testing.T, h http.HandlerFunc, maxHeader int) (addr string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	if maxHeader > 0 {
		srv.Config.MaxHeaderBytes = maxHeader
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	old := probeRoots
	probeRoots = pool
	t.Cleanup(func() { probeRoots = old })
	return srv.Listener.Addr().String()
}

func TestDataProbeSendsFillerAndCountsData(t *testing.T) {
	var headerBytes atomic.Int64
	addr := tlsSite(t, func(w http.ResponseWriter, r *http.Request) {
		n := 0
		for k, v := range r.Header {
			n += len(k) + len(strings.Join(v, ""))
		}
		headerBytes.Store(int64(n))
		w.Write(make([]byte, 100<<10))
	}, 0)
	var d net.Dialer
	n, err := probeData(context.Background(), d.DialContext, addr, "example.com")
	if err != nil || n < siteDataLimit {
		t.Fatalf("%d %v", n, err)
	}
	if headerBytes.Load() < 20<<10 {
		t.Fatalf("request headers %d bytes", headerBytes.Load())
	}
}

func TestDataProbeAsksAgainWithoutFillerWhenRefused(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	addr := tlsSite(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RawQuery)
		mu.Unlock()
		w.Write(make([]byte, 70<<10))
	}, 4<<10) // large headers get 431
	var d net.Dialer
	n, err := probeData(context.Background(), d.DialContext, addr, "example.com")
	mu.Lock()
	defer mu.Unlock()
	if err != nil || n < siteDataLimit || len(paths) != 1 || len(paths[0]) < 5000 {
		t.Fatalf("%d %v, requests %d", n, err, len(paths))
	}
}

func TestOtherNameCheckTellsAnswerFromSilence(t *testing.T) {
	addr := tlsSite(t, func(http.ResponseWriter, *http.Request) {}, 0)
	var d net.Dialer
	if err := probeOtherName(context.Background(), d.DialContext, addr, "example.com"); err != nil {
		t.Fatalf("answering server: %v", err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // takes the hello, says nothing
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := probeOtherName(ctx, d.DialContext, ln.Addr().String(), "example.com"); err == nil || shortError(err) != "silent" {
		t.Fatalf("silent server: %v", err)
	}
}

// A site's network ways are checked one after another.
func TestSiteChecksRunInTurn(t *testing.T) {
	tt := newBrowserTunnel(t, []SiteGroup{{Via: "smart", Hosts: []string{"example.org"}}}, nil, nil)
	tt.cfg.Canaries, tt.cfg.References = nil, nil
	saved := diagTelegramDCs
	diagTelegramDCs = nil
	defer func() { diagTelegramDCs = saved }()
	ok := echoDialer("x:", new(atomic.Int32))
	tt.ways[RouteDirect] = way{ok, time.Second}
	tt.strategies = []strategy{{"a", ok}, {"b", ok}, {"c", ok}}
	var inFlight, most atomic.Int32
	enter := func() {
		n := inFlight.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)
	}
	tt.siteDataProbe = func(context.Context, dialFunc, string, string) (int, error) { enter(); return 64 << 10, nil }
	tt.siteOtherProbe = func(context.Context, dialFunc, string, string) error { enter(); return errors.New("silent") }
	tt.cfg.References = []string{"ref.example.com:443"}
	tt.probeTLS = func(context.Context, string, string) error { return nil }
	tt.smartProbe = func(context.Context, dialFunc, string, string) error { return nil }
	d := tt.Diagnose(context.Background())
	if most.Load() != 1 {
		t.Fatalf("%d checks at once", most.Load())
	}
	var row DiagRow
	for _, r := range d.Rows {
		if r.Kind == "site" {
			row = r
		}
	}
	if row.Kind != "site" || row.Results[0].KB != 64 || d.Ways[len(d.Ways)-1] != diagOtherNameCol || row.Results[len(row.Results)-1].Error != "silent" {
		t.Fatalf("%v %+v", d.Ways, row)
	}
}

// Without the browser port (Android in this release) no site rows appear.
func TestNoSiteRowsWithoutTheBrowserPort(t *testing.T) {
	tt := newTestTunnel(t, ModeAuto, nil, nil)
	tt.cfg.Sites = testSites
	tt.cfg.Canaries, tt.cfg.References = nil, nil
	tt.sites = newSiteSet(tt.cfg.SiteGroups())
	saved := diagTelegramDCs
	diagTelegramDCs = nil
	defer func() { diagTelegramDCs = saved }()
	for _, r := range tt.Diagnose(context.Background()).Rows {
		if r.Kind == "site" {
			t.Fatal("site rows without the browser port")
		}
	}
}

// A server that closes the connection on the filler without a word is
// asked again without it.
func TestDataProbeAsksAgainWhenTheServerCloses(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS() // only for its certificate
	cfg := srv.TLS.Clone()
	srv.Close()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	old := probeRoots
	probeRoots = pool
	defer func() { probeRoots = old }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 64<<10)
				n, _ := c.Read(buf)
				if strings.Contains(string(buf[:n]), "X-Pad") {
					return // closes without an answer
				}
				c.Write([]byte("HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n"))
				c.Write(make([]byte, 70<<10))
			}()
		}
	}()
	var d net.Dialer
	n, err := probeData(context.Background(), d.DialContext, ln.Addr().String(), "example.com")
	if err != nil || n < siteDataLimit {
		t.Fatalf("%d %v", n, err)
	}
}

func TestTunnelColumnKeepsTheDataSize(t *testing.T) {
	tt := newBrowserTunnel(t, []SiteGroup{{Via: "tunnel", Hosts: []string{"example.net"}}}, nil, nil)
	tt.cfg.Canaries, tt.cfg.References = nil, nil
	saved := diagTelegramDCs
	diagTelegramDCs = nil
	defer func() { diagTelegramDCs = saved }()
	tt.ways[RouteTunnel] = way{echoDialer("t:", new(atomic.Int32)), time.Second}
	tt.siteDataProbe = func(context.Context, dialFunc, string, string) (int, error) { return 64 << 10, nil }
	d := tt.Diagnose(context.Background())
	col := slices.Index(d.Ways, "soar")
	for _, r := range d.Rows {
		if r.Kind == "site" && r.Results[col].KB != 64 {
			t.Fatalf("%+v", r.Results[col])
		}
	}
}

func TestSiteRowErrorsDoNotQuoteTheName(t *testing.T) {
	tt := newBrowserTunnel(t, []SiteGroup{{Via: "smart", Hosts: []string{"private.example.org"}}}, nil, nil)
	tt.cfg.Canaries, tt.cfg.References = nil, nil
	saved := diagTelegramDCs
	diagTelegramDCs = nil
	defer func() { diagTelegramDCs = saved }()
	tt.ways[RouteDirect] = way{echoDialer("d:", new(atomic.Int32)), time.Second}
	tt.siteDataProbe = func(context.Context, dialFunc, string, string) (int, error) {
		return 0, errors.New("x: no route to private.example.org at 192.0.2.1")
	}
	b, _ := json.Marshal(tt.Diagnose(context.Background()))
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "192.0.2.1") {
		t.Fatalf("%s", b)
	}
}
