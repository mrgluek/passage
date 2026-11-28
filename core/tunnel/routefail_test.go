package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

func TestTransportStage(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"tls alert":    {tls.AlertError(40), "tls"},
		"tls text":     {errors.New("remote error: tls: handshake failure"), "tls"},
		"dns":          {&net.DNSError{Err: "no such host", Name: "example.org"}, "dns"},
		"deadline":     {context.DeadlineExceeded, "timeout"},
		"dial refused": {&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "connect"},
		"reset":        {&net.OpError{Op: "read", Err: syscall.ECONNRESET}, "reset"},
		"eof":          {io.ErrUnexpectedEOF, "reset"},
		"other":        {errors.New("boom"), "network"},
	} {
		if got := transportStage(tc.err); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestRouteFailureKeepsTheSentinel(t *testing.T) {
	err := failed(errHTTPFailed, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})
	if !errors.Is(err, errHTTPFailed) || errors.Is(err, errHTTPRefused) || err.Error() != "HTTP request failed (connect)" {
		t.Fatal(err)
	}
	if strings.Contains(failed(errWSSFailed, &net.OpError{Op: "dial", Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 443}, Err: syscall.ECONNREFUSED}).Error(), "192.0.2.1") {
		t.Fatal("address in the error text")
	}
}

// A server that answers the upgrade with a status is told apart from a cut.
func TestWSSUpgradeRefusalAndCutAreDistinct(t *testing.T) {
	key := make([]byte, 32)
	refuse := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) }))
	defer refuse.Close()
	c, err := NewWSSClient(links.WSSParams{Endpoint: "wss://" + strings.TrimPrefix(refuse.URL, "https://"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	c.httpClient = refuse.Client()
	_, err = c.dialWSSSession(context.Background())
	if !errors.Is(err, errWSSUpgrade) || errors.Is(err, errWSSFailed) || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("refusal: %v", err)
	}
	// A server that is not TLS at all: the TLS stage fails, no HTTP answer.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plain.Close()
	c2, _ := NewWSSClient(links.WSSParams{Endpoint: "wss://" + strings.TrimPrefix(plain.URL, "http://"), Key: key})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c2.dialWSSSession(ctx)
	if !errors.Is(err, errWSSFailed) || !strings.Contains(err.Error(), "(tls)") {
		t.Fatalf("tls cut: %v", err)
	}
}

func TestGoneRouteCoolsLongAndIsNoVerdict(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	gone := refusedWith(errWSSUpgrade, 404)
	if !routeGone(gone) || !routeGone(refusedWith(errWSSUpgrade, 410)) || routeGone(refusedWith(errWSSUpgrade, 403)) || routeGone(errors.New("x")) {
		t.Fatal("routeGone")
	}
	bad := &poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return nil, gone }}
	good := &poolFixture{dial: poolSuccess}
	p := &routePool{clients: []soarClient{bad, good}, ipv6: []bool{false, false}, now: func() time.Time { return now }}
	conn, err := p.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if got := p.failedUntil[0].Sub(now); got != routeGoneCooldown {
		t.Fatalf("cooldown = %v", got)
	}
	clock := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&clock)
	tt.recordFailedDial(g, "br", gone, 0)
	if g.lastConnResult.Outcome != "" {
		t.Fatal("a gone route became the last result", g.lastConnResult)
	}
	tt.recordFailedDial(g, "br", refusedWith(errWSSUpgrade, 403), 0)
	if g.lastConnResult.Outcome != "refused" {
		t.Fatal("other refusals still count", g.lastConnResult)
	}
}

func TestDestinationRefusalIsNoGroupResult(t *testing.T) {
	clock := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&clock)
	tt.recordFailedDial(g, "berry-uk", fmt.Errorf("x: %w", errDestinationRefused), 0)
	tt.recordFailedDial(g, "berry-uk", errDestinationUnreachable, 0)
	if g.lastConnResult.Outcome != "" || !g.lastConn.IsZero() {
		t.Fatal("policy refusal recorded", g.lastConnResult)
	}
	tt.recordConnection(g, "ok", "berry-de")
	tt.recordFailedDial(g, "berry-uk", errors.New("reset"), 0)
	if g.lastConnOK.IsZero() || g.lastConnResult.Outcome != "refused" {
		t.Fatal("ok time or failure lost", g.lastConnOK, g.lastConnResult)
	}
}
