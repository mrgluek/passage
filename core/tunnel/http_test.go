package tunnel

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	links "github.com/mrgluek/passage-link/link"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
	// Use the production transport, trusting the test server's certificate.
	client.httpClient.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
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
	if err := httpDownload(bytes.NewReader(packet), &out, func() {}); err != io.EOF || out.String() != "abc" {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := httpDownload(bytes.NewReader([]byte{0, 3, 1}), io.Discard, func() {}); err == nil {
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

func TestHTTPSNIOverridesEndpointHost(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS.ServerName != "example.com" || r.Host != r.URL.Query().Get("host") {
			t.Errorf("SNI %q, Host %q", r.TLS.ServerName, r.Host)
		}
		if r.ProtoMajor != 2 {
			t.Errorf("HTTP version: %s", r.Proto)
		}
		w.Write([]byte{1})
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	ip, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	for _, sni := range []string{"example.com", "example.net"} {
		client, err := NewHTTPClient(links.HTTPParams{Endpoint: "https://example.org:" + port + "/passage", Key: wssTestKey, DialIP: ip, SNI: sni})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		transport := client.httpClient.Transport.(*http.Transport)
		transport.TLSClientConfig.RootCAs = x509.NewCertPool()
		transport.TLSClientConfig.RootCAs.AddCert(server.Certificate())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := httpRequest(ctx, client.httpClient, client.params.Endpoint, "", url.Values{"host": {"example.org:" + port}})
		if resp != nil {
			resp.Body.Close()
		}
		cancel()
		if sni == "example.com" && err != nil || sni == "example.net" && err == nil {
			t.Fatalf("SNI %q: %v", sni, err)
		}
	}
}

func TestHTTPUploadWindowOrderingAndRetries(t *testing.T) {
	local, bridge := net.Pipe()
	defer local.Close()
	defer bridge.Close()
	exit, err := NewHTTPExit(wssTestKey, "/passage", func(string, uint16) (net.Conn, byte) { return nil, 1 })
	if err != nil {
		t.Fatal(err)
	}
	defer exit.Close()
	entry := &httpExitSession{conn: bridge, cancel: func() {}}
	exit.sessions["session"] = entry
	received := make(chan string, 1)
	go func() {
		var data bytes.Buffer
		io.Copy(&data, local)
		received <- data.String()
	}()
	upload := func(seq uint64, data string, status int) {
		t.Helper()
		q := url.Values{"n": {strconv.FormatUint(seq, 10)}, "d": {base64.RawURLEncoding.EncodeToString([]byte(data))}}
		r := httptest.NewRequest(http.MethodGet, "/passage?"+q.Encode(), nil)
		r.Header.Set(httpSessionHeader, "session")
		w := httptest.NewRecorder()
		exit.ServeHTTP(w, r)
		if w.Code != status || status == http.StatusOK && !bytes.Equal(w.Body.Bytes(), []byte{1}) {
			t.Fatalf("seq %d: status %d, body %q", seq, w.Code, w.Body.String())
		}
	}
	upload(1, "b", http.StatusOK)
	upload(1, "b", http.StatusOK)
	upload(1, "wrong", http.StatusNotFound)
	upload(httpUploadWindow, "outside", http.StatusNotFound)
	if entry.next != 0 || len(entry.pending) != 1 {
		t.Fatalf("next %d, buffered %d", entry.next, len(entry.pending))
	}
	upload(0, "a", http.StatusOK)
	upload(0, "a", http.StatusOK)
	upload(1, "b", http.StatusOK)
	upload(0, "wrong", http.StatusNotFound)
	for seq := uint64(2); seq < 8; seq++ {
		upload(seq, string(byte('a'+seq)), http.StatusOK)
	}
	upload(1, "b", http.StatusNotFound)
	upload(2, "c", http.StatusOK)
	upload(8+httpUploadWindow, "outside", http.StatusNotFound)
	if len(entry.pending) != 0 || len(entry.hashes) != httpUploadWindow {
		t.Fatalf("buffered %d, hashes %d", len(entry.pending), len(entry.hashes))
	}
	bridge.Close()
	if got := <-received; got != "abcdefgh" {
		t.Fatalf("received %q", got)
	}
}

func TestHTTPConcurrentUploadWindow(t *testing.T) {
	client, _, _ := httpFixture(t)
	base := client.httpClient.Transport
	var active, peak, count atomic.Int32
	var armed atomic.Bool
	release := make(chan struct{})
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.RawQuery == "" || !armed.Load() {
			return base.RoundTrip(r)
		}
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		if count.Add(1) == httpUploadWindow {
			close(release)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return base.RoundTrip(r)
	})
	conn := dialTest(t, client, "mail.example.org:443")
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	armed.Store(true)
	if err := wssRoundTrip(conn, bytes.Repeat([]byte("ordered upload"), 10000)); err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got != httpUploadWindow {
		t.Fatalf("peak concurrent uploads: %d", got)
	}
}

func TestHTTPUploadFailureCancelsWindow(t *testing.T) {
	client, _, _ := httpFixture(t)
	base := client.httpClient.Transport
	var armed atomic.Bool
	var count atomic.Int32
	finished := make(chan struct{}, httpUploadWindow)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.RawQuery == "" || !armed.Load() {
			return base.RoundTrip(r)
		}
		defer func() { finished <- struct{}{} }()
		if count.Add(1) == httpUploadWindow {
			return nil, errors.New("upload failed")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	conn := dialTest(t, client, "mail.example.org:443")
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	armed.Store(true)
	if _, err := conn.Write(bytes.Repeat([]byte("upload"), 10000)); err == nil {
		t.Fatal("failed upload did not stop the stream")
	}
	for i := 0; i < httpUploadWindow; i++ {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("upload request was not canceled")
		}
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

// frozenBody stops delivering after limit bytes without ending, as a
// stalled connection does.
type frozenBody struct {
	io.ReadCloser
	ctx   context.Context
	limit int
}

func (b *frozenBody) Read(p []byte) (int, error) {
	if b.limit <= 0 {
		<-b.ctx.Done()
		return 0, b.ctx.Err()
	}
	n, err := b.ReadCloser.Read(p[:min(len(p), b.limit)])
	b.limit -= n
	return n, err
}

func TestHTTPSilentDownloadEndsSession(t *testing.T) {
	client, _, _ := httpFixture(t)
	base := client.httpClient.Transport
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if err == nil && r.URL.RawQuery == "" {
			// Past the salt and HELLO.
			resp.Body = &frozenBody{ReadCloser: resp.Body, ctx: r.Context(), limit: 64}
		}
		return resp, err
	})
	client.httpSilence = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, st, err := client.reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	select {
	case <-s.done:
	case <-ctx.Done():
		t.Fatal("silent session stayed up")
	}
}
