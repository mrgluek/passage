package tunnel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	links "github.com/mrgluek/passage-link/link"
)

var wssTestKey = bytes.Repeat([]byte{7}, 32)

// wssExit is a reference exit: it checks the token, then relays each stream
// to a loopback TCP server that runs handler.
type wssExit struct {
	*httptest.Server
	mu       sync.Mutex
	sessions []*wssSession
	opened   int
}

func wssFixture(t *testing.T, limit int, handler func(net.Conn)) (*WSSClient, *wssExit) {
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
			go func() { defer c.Close(); handler(c) }()
		}
	}()
	exit := &wssExit{}
	exit.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Sec-WebSocket-Protocol")
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(raw) != 24+wssTag {
			http.NotFound(w, r)
			return
		}
		mac := hmac.New(sha256.New, wssKey(wssTestKey, nil, "auth"))
		mac.Write(raw[:24])
		sent := time.Unix(int64(binary.BigEndian.Uint64(raw[16:24])), 0)
		if !hmac.Equal(mac.Sum(nil)[:wssTag], raw[24:]) || time.Since(sent).Abs() > 5*time.Minute {
			http.NotFound(w, r)
			return
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{token}})
		if err != nil {
			return
		}
		salt := make([]byte, wssSalt)
		rand.Read(salt)
		rec := &wssRecords{open: wssAEAD(wssKey(wssTestKey, raw[:16], "c2s")), seal: wssAEAD(wssKey(wssTestKey, slices.Concat(raw[:16], salt), "s2c"))}
		s := newWSSSession(context.Background(), ws, rec, limit, func(host string, port uint16) (net.Conn, byte) {
			exit.mu.Lock()
			exit.opened++
			exit.mu.Unlock()
			if strings.HasPrefix(host, "denied.") {
				return nil, 1
			}
			if strings.HasPrefix(host, "unreachable.") {
				return nil, 2
			}
			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				return nil, 2
			}
			return c, 0
		}, nil)
		exit.mu.Lock()
		exit.sessions = append(exit.sessions, s)
		exit.mu.Unlock()
		if _, err = rec.conn.Write(salt); err != nil || s.send(frameHello, 0, binary.BigEndian.AppendUint16(nil, uint16(limit))) != nil {
			s.fail(io.ErrUnexpectedEOF)
			return
		}
		s.run(nil)
	}))
	t.Cleanup(exit.Close)
	t.Cleanup(func() {
		exit.mu.Lock()
		defer exit.mu.Unlock()
		for _, s := range exit.sessions {
			s.fail(net.ErrClosed)
		}
	})
	client, err := NewWSSClient(links.WSSParams{Endpoint: "wss" + strings.TrimPrefix(exit.URL, "https") + "/passage", Key: wssTestKey})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = exit.Client()
	t.Cleanup(func() { client.Close() })
	return client, exit
}
func (e *wssExit) count() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.sessions) }
func (e *wssExit) kill() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.sessions {
		s.ws.CloseNow()
	}
}
func echo(c net.Conn) { io.Copy(c, c) }
func dialTest(t *testing.T, client *WSSClient, target string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn
}
func wssRoundTrip(conn net.Conn, payload []byte) error {
	sent := make(chan error, 1)
	go func() { _, err := conn.Write(payload); sent <- err }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if err := <-sent; err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return errors.New("payload changed")
	}
	return nil
}

func TestWSSStreamSurvivesDialContextAndExceedsWindow(t *testing.T) {
	client, _ := wssFixture(t, 6, echo)
	conn := dialTest(t, client, "mail.example.org:443")
	// Larger than the window, so it needs window updates both ways.
	if err := wssRoundTrip(conn, bytes.Repeat([]byte("stream"), 200000)); err != nil {
		t.Fatal(err)
	}
	if !client.Established() {
		t.Fatal("active route not established")
	}
	client.Close()
	if client.Established() {
		t.Fatal("closed route still established")
	}
	if _, err := conn.Write([]byte{1}); err == nil {
		t.Fatal("write after close succeeded")
	}
	if _, err := client.DialContext(context.Background(), "tcp", "mail.example.org:443"); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
}
func TestWSSStreamsShareOneSession(t *testing.T) {
	client, exit := wssFixture(t, 6, echo)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn := dialTest(t, client, "mail.example.org:443")
			errs <- wssRoundTrip(conn, bytes.Repeat([]byte{byte(i)}, 100000+i))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := exit.count(); n != 1 {
		t.Fatalf("%d sessions, want 1", n)
	}
}
func TestWSSSessionLimitOpensAnotherSession(t *testing.T) {
	client, exit := wssFixture(t, 2, echo)
	for range 3 {
		dialTest(t, client, "mail.example.org:443")
	}
	if n := exit.count(); n != 2 {
		t.Fatalf("%d sessions, want 2", n)
	}
}
func TestWSSSlowStreamDoesNotBlockOthers(t *testing.T) {
	client, _ := wssFixture(t, 6, func(c net.Conn) {
		var b [1]byte
		if _, err := c.Read(b[:]); err != nil {
			return
		}
		if b[0] == 'f' { // flood: far more than one window, never read by the client
			c.Write(make([]byte, 4*wssWindow))
			return
		}
		c.Write(b[:])
		io.Copy(c, c)
	})
	flood := dialTest(t, client, "mail.example.org:443")
	if _, err := flood.Write([]byte("f")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	conn := dialTest(t, client, "mail.example.org:443")
	if err := wssRoundTrip(conn, []byte("still flowing")); err != nil {
		t.Fatal(err)
	}
}
func TestWSSWriteEOFKeepsReplies(t *testing.T) {
	client, _ := wssFixture(t, 6, echo)
	conn := dialTest(t, client, "mail.example.org:443")
	data := bytes.Repeat([]byte("eof\n"), 100000)
	go func() {
		conn.Write(data)
		conn.(interface{ CloseWrite() error }).CloseWrite()
	}()
	got, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("got %d bytes, %v", len(got), err)
	}
	waitFor(t, func() bool { return !client.Established() })
}
func TestWSSRejectsUntrustedTLSAndWrongKey(t *testing.T) {
	client, _ := wssFixture(t, 6, echo)
	trusted := client.httpClient
	client.httpClient = &http.Client{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.DialContext(ctx, "tcp", "mail.example.org:443"); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	client.httpClient = trusted
	client.params.Key = bytes.Repeat([]byte{8}, 32)
	if _, err := client.DialContext(ctx, "tcp", "mail.example.org:443"); err == nil {
		t.Fatal("wrong key accepted")
	}
}
func TestWSSDestinationFailureDoesNotPenalizeRoute(t *testing.T) {
	client, _ := wssFixture(t, 6, echo)
	pool := &routePool{clients: []soarClient{client}, ipv6: []bool{false}, kinds: []string{"wss"}, ids: []string{"web"}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.DialContext(ctx, "tcp", "denied.example.org:443"); !errors.Is(err, errDestinationRefused) {
		t.Fatal(err)
	}
	if _, err := client.DialContext(ctx, "tcp", "denied.example.org:443"); errors.Is(err, errDestinationUnreachable) {
		t.Fatal("policy refusal reported as unreachable")
	}
	if _, err := client.DialContext(ctx, "tcp", "unreachable.example.org:443"); !errors.Is(err, errDestinationUnreachable) || !errors.Is(err, errDestinationRefused) {
		t.Fatal(err)
	}
	pool.DialContext(ctx, "tcp", "denied.example.org:443")
	if len(pool.failedUntil) != 0 {
		t.Fatal("destination failure penalized route")
	}
	conn, err := pool.DialContext(ctx, "tcp", "mail.example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if connVia(conn) != "wss" {
		t.Fatal("missing WSS status")
	}
}
func TestWSSPendingHandshakeCanceledByClose(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{r.Header.Get("Sec-WebSocket-Protocol")}})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		close(entered)
		for {
			if _, _, err = ws.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	client, err := NewWSSClient(links.WSSParams{Endpoint: "wss" + strings.TrimPrefix(server.URL, "https"), Key: wssTestKey})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = server.Client()
	defer client.Close()
	done := make(chan error, 1)
	go func() { _, err := client.DialContext(context.Background(), "tcp", "mail.example.org:443"); done <- err }()
	<-entered
	client.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("pending handshake succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending handshake leaked")
	}
}
func TestWSSTruncatedStreamIsNotEOF(t *testing.T) {
	client, exit := wssFixture(t, 6, func(c net.Conn) {
		c.Write([]byte("partial"))
		time.Sleep(time.Second)
	})
	conn := dialTest(t, client, "mail.example.org:443")
	got := make([]byte, 7)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "partial" {
		t.Fatalf("got %q, %v", got, err)
	}
	exit.kill()
	if _, err := conn.Read(got); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}
func TestWSSIdleSessionCloses(t *testing.T) {
	defer func(d time.Duration) { wssIdle = d }(wssIdle)
	wssIdle = 50 * time.Millisecond
	client, _ := wssFixture(t, 6, echo)
	conn := dialTest(t, client, "mail.example.org:443")
	conn.Close()
	waitFor(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.sessions) == 0 })
}
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
	}
}
func TestWSSExitInterop(t *testing.T) {
	endpoint := os.Getenv("PASSAGE_WSS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("local exit runtime not requested")
	}
	client, err := NewWSSClient(links.WSSParams{Endpoint: "wss://example.org/passage", Key: wssTestKey})
	if err != nil {
		t.Fatal(err)
	}
	// Only this local runtime test permits plaintext loopback WebSockets.
	if !strings.HasPrefix(endpoint, "ws://127.0.0.1:") {
		t.Fatal("expected loopback runtime")
	}
	client.params.Endpoint = endpoint
	defer client.Close()
	target := os.Getenv("PASSAGE_WSS_TEST_TARGET")
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn := dialTest(t, client, target)
			data := bytes.Repeat([]byte(fmt.Sprintf("exit interop %d\n", i)), 20000)
			go func() {
				conn.Write(data)
				conn.(interface{ CloseWrite() error }).CloseWrite()
			}()
			got, err := io.ReadAll(io.LimitReader(conn, int64(len(data)+1)))
			if err == nil && !bytes.Equal(data, got) {
				err = fmt.Errorf("got %d bytes, want %d", len(got), len(data))
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	client.mu.Lock()
	sessions := len(client.sessions)
	client.mu.Unlock()
	if sessions != 1 {
		t.Fatalf("%d sessions, want 1", sessions)
	}
	other, err := NewWSSClient(links.WSSParams{Endpoint: "wss://example.org/passage", Key: bytes.Repeat([]byte{8}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	other.params.Endpoint = endpoint
	defer other.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = other.DialContext(ctx, "tcp", target); err == nil {
		t.Fatal("exit accepted wrong key")
	}
}

func TestWSSLinkImportsAndBuildsRoutePool(t *testing.T) {
	route := links.Route{ID: "web", Type: "wss", Wire: "wss-v1", Priority: 20, Params: map[string]any{"endpoint": "wss://example.org/passage", "key": wssTestKey}}
	raw, err := links.Encode(links.Bundle{V: 2, Kind: "routes", Mode: "merge", Routes: []links.Route{route}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := links.URI(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ImportLink(Config{}, uri, false)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Count != 1 || imported.Config.Routes[0].Type != "wss" {
		t.Fatal("WSS import failed")
	}
	client, err := newRoutePool(imported.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	pool := client.(*routePool)
	if _, ok := pool.clients[0].(*WSSClient); !ok {
		t.Fatal("WSS adapter not installed")
	}
	if pool.ways()[0].name != "wss" {
		t.Fatal("WSS diagnostics missing")
	}
}
func TestWSSRotatesSessionAfterTraffic(t *testing.T) {
	defer func(n int64) { wssRotate = n }(wssRotate)
	wssRotate = 64 << 10
	client, exit := wssFixture(t, 6, echo)
	old := dialTest(t, client, "mail.example.org:443")
	if err := wssRoundTrip(old, make([]byte, 40<<10)); err != nil {
		t.Fatal(err)
	}
	fresh := dialTest(t, client, "mail.example.org:443")
	if n := exit.count(); n != 2 {
		t.Fatalf("%d sessions, want 2", n)
	}
	// The drained session keeps serving its stream until it closes.
	if err := wssRoundTrip(old, []byte("still here")); err != nil {
		t.Fatal(err)
	}
	old.Close()
	waitFor(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.sessions) == 1 })
	if err := wssRoundTrip(fresh, []byte("fresh session")); err != nil {
		t.Fatal(err)
	}
}
