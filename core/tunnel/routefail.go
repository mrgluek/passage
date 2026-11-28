package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
)

// routeFailure is a route error with the stage that failed, so reports tell
// a blocked connect from a TLS cut, an upgrade refusal and a server refusal.
// errors.Is still matches the base error. The text carries no addresses.
type routeFailure struct {
	base  error
	stage string
}

func (e *routeFailure) Error() string { return e.base.Error() + " (" + e.stage + ")" }
func (e *routeFailure) Unwrap() error { return e.base }

// failed wraps base with the stage that cause points to.
func failed(base, cause error) error { return &routeFailure{base, transportStage(cause)} }

// refusedWith wraps base with the HTTP status the server answered.
func refusedWith(base error, status int) error {
	return &routeFailure{base, fmt.Sprintf("HTTP %d", status)}
}

// transportStage says where a failed HTTP or WebSocket exchange broke:
// dns, connect, tls, timeout or reset (anything after the connection opened).
func transportStage(err error) string {
	var (
		verify  *tls.CertificateVerificationError
		alert   tls.AlertError
		record  tls.RecordHeaderError
		unknown x509.UnknownAuthorityError
		dns     *net.DNSError
		op      *net.OpError
		timeout net.Error
	)
	switch {
	case err == nil:
		return "network"
	case errors.As(err, &verify), errors.As(err, &alert), errors.As(err, &record), errors.As(err, &unknown), strings.Contains(err.Error(), "tls:"), strings.Contains(err.Error(), "HTTP response to HTTPS client"):
		return "tls"
	case errors.As(err, &dns):
		return "dns"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return "timeout"
	case errors.As(err, &op) && op.Op == "dial":
		return "connect"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "reset"
	}
	return "network"
}

// routeGone reports an upgrade answered with 404 or 410: the endpoint no
// longer serves this route, so retrying it soon is wasted work.
func routeGone(err error) bool {
	var f *routeFailure
	return errors.As(err, &f) && errors.Is(f.base, errWSSUpgrade) && (f.stage == "HTTP 404" || f.stage == "HTTP 410")
}
