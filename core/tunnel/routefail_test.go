package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
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
