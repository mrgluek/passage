package tunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	links "github.com/mrgluek/passage-link/link"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func httpFixture(t *testing.T) (*WSSClient, *HTTPExit, *httptest.Server) {
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
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	exit, err := NewHTTPExit(wssTestKey, "/passage", func(host string, port uint16) (net.Conn, byte) {
		if host != "mail.example.org" || port != 443 {
			return nil, 1
		}
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			return nil, 2
		}
		return conn, 0
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.ContentLength != 0 || len(r.RequestURI) > 6000 {
			t.Error("request exceeds CDN limits")
			http.Error(w, "refused", 413)
			return
		}
		exit.ServeHTTP(w, r)
	}))
	client, err := NewHTTPClient(links.HTTPParams{Endpoint: server.URL + "/passage", Key: wssTestKey})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = server.Client()
	t.Cleanup(func() { client.Close(); exit.Close(); server.Close() })
	return client, exit, server
}
func TestHTTPConcurrentStreamsAndFlowControl(t *testing.T) {
	client, exit, _ := httpFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		conn := dialTest(t, client, "mail.example.org:443")
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			if err := wssRoundTrip(conn, bytes.Repeat([]byte("http"), 150000)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	exit.mu.Lock()
	defer exit.mu.Unlock()
	if len(exit.sessions) != 1 {
		t.Fatalf("got %d sessions", len(exit.sessions))
	}
}
func TestHTTPHalfCloseAndPolicy(t *testing.T) {
	client, _, _ := httpFixture(t)
	if _, err := client.DialContext(context.Background(), "tcp", "other.example.org:443"); err != errDestinationRefused {
		t.Fatalf("policy: %v", err)
	}
	conn := dialTest(t, client, "mail.example.org:443")
	defer conn.Close()
	if err := wssRoundTrip(conn, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := conn.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("EOF: %d %v", n, err)
	}
}
func TestHTTPAuthenticationAndTLS(t *testing.T) {
	client, _, _ := httpFixture(t)
	trusted := client.httpClient
	client.httpClient = &http.Client{}
	if _, err := client.DialContext(context.Background(), "tcp", "mail.example.org:443"); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	client.httpClient = trusted
	client.params.Key = bytes.Repeat([]byte{9}, 32)
	if _, err := client.DialContext(context.Background(), "tcp", "mail.example.org:443"); err == nil {
		t.Fatal("wrong key accepted")
	}
}
func TestHTTPCloseCancelsHandshake(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	client, err := NewHTTPClient(links.HTTPParams{Endpoint: server.URL + "/passage", Key: wssTestKey})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = server.Client()
	done := make(chan error, 1)
	go func() { _, err := client.DialContext(context.Background(), "tcp", "mail.example.org:443"); done <- err }()
	<-started
	client.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("handshake did not stop")
	}
}
func TestHTTPIdleChunksAndTruncation(t *testing.T) {
	packet := binary.BigEndian.AppendUint16(nil, 0)
	packet = binary.BigEndian.AppendUint16(packet, 3)
	packet = append(packet, "abc"...)
	var out bytes.Buffer
	if err := httpDownload(bytes.NewReader(packet), &out); err != io.EOF || out.String() != "abc" {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := httpDownload(bytes.NewReader([]byte{0, 3, 1}), io.Discard); err == nil {
		t.Fatal("truncated download accepted")
	}
}
func TestHTTPLinkAndMode(t *testing.T) {
	r := links.Route{ID: "cdn", Type: "http", Wire: "http-v1", Priority: 20, Params: map[string]any{"endpoint": "https://example.org/passage", "key": wssTestKey, "dial_ip": "192.0.2.1"}}
	text, err := links.Encode(links.Bundle{V: 2, Kind: "routes", Mode: "merge", Routes: []links.Route{r}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := links.URI(text, "")
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ImportLink(Config{}, uri, false)
	if err != nil {
		t.Fatal(err)
	}
	imported.Config.Mode = "http"
	cfg, err := imported.Config.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := newRoutePool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if pool.(*routePool).ways()[0].name != "http" {
		t.Fatal("HTTP diagnostics missing")
	}

	cfg.My = "socks5://127.0.0.1:9050"
	combined, err := newRoutePool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer combined.Close()
	ways := combined.(*routePool).ways()
	if len(ways) != 2 || ways[0].name != "my" || ways[1].name != "http" {
		t.Fatal("HTTP import lost My route or its priority")
	}
}
func TestHTTPRepeatedUploadAndSessionReplay(t *testing.T) {
	client, _, server := httpFixture(t)
	base := client.httpClient.Transport
	var replayed atomic.Bool
	client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if err == nil && strings.Contains(r.URL.RawQuery, "d=") && !replayed.Swap(true) {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return base.RoundTrip(r.Clone(r.Context()))
		}
		return resp, err
	})}
	conn := dialTest(t, client, "mail.example.org:443")
	defer conn.Close()
	if err := wssRoundTrip(conn, []byte("only once")); err != nil {
		t.Fatal(err)
	}
	token, _ := wssToken(client.params.Key, time.Now())
	resp, err := httpRequest(context.Background(), server.Client(), server.URL+"/passage", token, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := httpRequest(context.Background(), server.Client(), server.URL+"/passage", token, nil); err == nil {
		t.Fatal("replayed session accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPPinnedIPPreservesTLSName(t *testing.T) {
	_, _, server := httpFixture(t)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewHTTPClient(links.HTTPParams{Endpoint: "https://example.com:" + port + "/passage", Key: wssTestKey, DialIP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	transport := client.httpClient.Transport.(*http.Transport)
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	conn := dialTest(t, client, "mail.example.org:443")
	defer conn.Close()
	if err := wssRoundTrip(conn, []byte("verified TLS name")); err != nil {
		t.Fatal(err)
	}
}
func TestHTTPBrokenDownloadTruncatesStreams(t *testing.T) {
	client, exit, _ := httpFixture(t)
	conn := dialTest(t, client, "mail.example.org:443")
	defer conn.Close()
	exit.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || err == io.EOF {
		t.Fatalf("truncated stream returned %v", err)
	}
}
