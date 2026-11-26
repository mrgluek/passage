package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

// pinnedFixture returns a client whose pinned address (192.0.2.1) leads to a
// server that completes TLS and then resets the request, and whose resolver
// answers 192.0.2.2 for the endpoint host, which leads to a working exit.
func pinnedFixture(t *testing.T, badHandler http.HandlerFunc) (*WSSClient, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	good, exit, goodServer := httpFixture(t)
	good.Close()
	_ = exit
	bad := httptest.NewTLSServer(badHandler)
	t.Cleanup(bad.Close)
	u, _ := url.Parse(goodServer.URL)
	client, err := NewHTTPClient(links.HTTPParams{Endpoint: "https://example.com:" + u.Port() + "/passage", Key: wssTestKey, DialIP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	client.httpClient.Transport.(*http.Transport).TLSClientConfig = goodServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	var lookups, dials atomic.Int32
	client.pin.lookup = func(ctx context.Context, host string) ([]string, error) {
		lookups.Add(1)
		return []string{"192.0.2.1", "192.0.2.2"}, nil
	}
	targets := map[string]string{"192.0.2.1": bad.Listener.Addr().String(), "192.0.2.2": goodServer.Listener.Addr().String()}
	client.pin.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, targets[host])
	}
	return client, &lookups, &dials
}

func resetHandler(w http.ResponseWriter, r *http.Request) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		c.Close()
	}
}

func TestPinnedFallbackAfterReset(t *testing.T) {
	client, lookups, _ := pinnedFixture(t, func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) })
	conn := dialTest(t, client, "mail.example.org:443")
	if err := wssRoundTrip(conn, []byte("via second address")); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if lookups.Load() != 1 || client.pin.current() != "192.0.2.2" {
		t.Fatalf("lookups=%d current=%s", lookups.Load(), client.pin.current())
	}
	conn = dialTest(t, client, "mail.example.org:443")
	defer conn.Close()
	if err := wssRoundTrip(conn, []byte("remembered")); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 1 {
		t.Fatalf("second session looked up again: %d", lookups.Load())
	}
}

func TestPinnedWorkingNoLookup(t *testing.T) {
	client, lookups, _ := pinnedFixture(t, resetHandler)
	client.pin.cands = []string{"192.0.2.2"}
	client.pin.pinned = "192.0.2.2"
	conn := dialTest(t, client, "mail.example.org:443")
	defer conn.Close()
	if err := wssRoundTrip(conn, []byte("pinned")); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 0 {
		t.Fatalf("lookups=%d", lookups.Load())
	}
}

func TestPinnedRefusalDoesNotFallBack(t *testing.T) {
	client, lookups, _ := pinnedFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) })
	if _, err := client.dialHTTPSession(context.Background()); err == nil || err.Error() != "HTTP request refused" {
		t.Fatalf("err=%v", err)
	}
	if lookups.Load() != 0 {
		t.Fatalf("lookups=%d", lookups.Load())
	}
}

func TestPinnedAllFailBounded(t *testing.T) {
	client, lookups, dials := pinnedFixture(t, resetHandler)
	client.pin.lookup = func(ctx context.Context, host string) ([]string, error) {
		lookups.Add(1)
		return []string{"192.0.2.1", "192.0.2.3"}, nil
	}
	start := time.Now()
	_, err := client.dialHTTPSession(context.Background())
	if !errors.Is(err, errHTTPFailed) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 15*time.Second || lookups.Load() != 1 || dials.Load() > 6 {
		t.Fatalf("elapsed=%v lookups=%d dials=%d", time.Since(start), lookups.Load(), dials.Load())
	}
	if client.pin.current() != "192.0.2.1" {
		t.Fatalf("did not return to pinned: %s", client.pin.current())
	}
}

func TestPinnedReturnsAfterTimeout(t *testing.T) {
	p := newPinnedAddr("192.0.2.1", "example.com")
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }
	p.lookup = func(context.Context, string) ([]string, error) { return []string{"192.0.2.2"}, nil }
	if !p.next(context.Background(), "192.0.2.1") || p.current() != "192.0.2.2" {
		t.Fatal("no move")
	}
	now = now.Add(pinnedReturn)
	if p.current() != "192.0.2.1" {
		t.Fatal("no return")
	}
}

func TestPinnedFallbackWSS(t *testing.T) {
	_, exit := wssFixture(t, 8, func(c net.Conn) { io.Copy(c, c) })
	bad := httptest.NewTLSServer(http.HandlerFunc(resetHandler))
	t.Cleanup(bad.Close)
	u, _ := url.Parse(exit.URL)
	client, err := NewWSSClient(links.WSSParams{Endpoint: "wss://example.com:" + u.Port() + "/passage", Key: wssTestKey, DialIP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	client.httpClient.Transport.(*http.Transport).TLSClientConfig = exit.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	var lookups atomic.Int32
	client.pin.lookup = func(context.Context, string) ([]string, error) {
		lookups.Add(1)
		return []string{"192.0.2.2"}, nil
	}
	targets := map[string]string{"192.0.2.1": bad.Listener.Addr().String(), "192.0.2.2": exit.Listener.Addr().String()}
	client.pin.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		return (&net.Dialer{}).DialContext(ctx, network, targets[host])
	}
	conn := dialTest(t, client, "mail.example.org:443")
	defer conn.Close()
	if err := wssRoundTrip(conn, []byte("wss second address")); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 1 || client.pin.current() != "192.0.2.2" {
		t.Fatalf("lookups=%d current=%s", lookups.Load(), client.pin.current())
	}
}

func TestPinnedDialAttemptHasOwnLimit(t *testing.T) {
	p := newPinnedAddr("192.0.2.1", "example.com")
	p.limit = 50 * time.Millisecond
	p.dial = func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.DialContext(parent, "tcp", "example.com:443"); err == nil || parent.Err() != nil {
		t.Fatalf("err=%v parent=%v", err, parent.Err())
	}
}

func TestPinnedLookupRefreshes(t *testing.T) {
	p := newPinnedAddr("192.0.2.1", "example.com")
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }
	answer := "192.0.2.2"
	p.lookup = func(context.Context, string) ([]string, error) { return []string{answer}, nil }
	if !p.next(context.Background(), "192.0.2.1") || p.current() != "192.0.2.2" {
		t.Fatal("first move")
	}
	answer = "192.0.2.9"
	now = now.Add(pinnedLookupTTL)
	if !p.next(context.Background(), "192.0.2.2") || p.current() != "192.0.2.9" {
		t.Fatalf("stale list kept: %s", p.current())
	}
}

func TestPinnedNoFallbackWhenSNIDiffers(t *testing.T) {
	c, err := NewHTTPClient(links.HTTPParams{Endpoint: "https://example.com/passage", Key: wssTestKey, DialIP: "192.0.2.1", SNI: "front.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.pin.host != "" {
		t.Fatalf("host %q would be looked up", c.pin.host)
	}
}
