package tunnel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func tlsProbeServer(t *testing.T, expired bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, _, _ := net.SplitHostPort(srv.Listener.Addr().String())
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"example.org"}, IPAddresses: []net.IP{net.ParseIP(host)},
		NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	if expired {
		cert.NotAfter = time.Now().Add(-time.Hour)
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	return srv
}

func TestTLSProbeReachability(t *testing.T) {
	for _, tc := range []struct {
		name                                        string
		trusted, wrongHost, expired, closed, wantOK bool
	}{
		{name: "private root", wantOK: true},
		{name: "trusted", trusted: true, wantOK: true},
		{name: "wrong host", wrongHost: true},
		{name: "trusted wrong host", trusted: true, wrongHost: true},
		{name: "expired", expired: true},
		{name: "trusted expired", trusted: true, expired: true},
		{name: "closed port", closed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tlsProbeServer(t, tc.expired)
			probeRoots = x509.NewCertPool()
			if tc.trusted {
				cert, err := x509.ParseCertificate(srv.TLS.Certificates[0].Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				probeRoots.AddCert(cert)
			}
			t.Cleanup(func() { probeRoots = nil })
			addr := srv.Listener.Addr().String()
			host, _, _ := net.SplitHostPort(addr)
			if tc.wrongHost {
				host = "wrong.example.org"
			}
			if tc.closed {
				srv.Close()
			}
			var d net.Dialer
			for _, via := range []bool{false, true} {
				t.Run(fmt.Sprintf("via=%v", via), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					probe := probeTLS
					if via {
						probe = func(ctx context.Context, addr, host string) error {
							return probeTLSVia(ctx, d.DialContext, addr, host)
						}
					}
					err := probe(ctx, addr, host)
					if got := probeReachable(err); got != tc.wantOK {
						t.Fatalf("reachable=%v, want %v: %v", got, tc.wantOK, err)
					}
					check := func(ctx context.Context, _, _ string) error { return probe(ctx, addr, host) }
					tt := &Tunnel{probeTLS: check, probeTunnel: check}
					if got := tt.probeHost(ctx, newGroup("example.org", kindRelay, RouteDirect), via, addr); (got == nil) != tc.wantOK {
						t.Fatalf("probeHost=%v, want reachable %v", got, tc.wantOK)
					}
					if tc.wantOK && !tc.trusted {
						var verification *tls.CertificateVerificationError
						if !errors.As(err, &verification) || shortError(err) != "answered, untrusted chain" {
							t.Fatalf("missing untrusted-chain diagnostic: %v", err)
						}
					}
				})
			}
		})
	}
}

func TestProbeReachableErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, true},
		{x509.UnknownAuthorityError{}, true},
		{fmt.Errorf("wrapped: %w", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}), true},
		{x509.HostnameError{}, false},
		{x509.CertificateInvalidError{Reason: x509.Expired}, false},
		{context.DeadlineExceeded, false},
		{errors.New("connection reset"), false},
		{errors.New("connection refused"), false},
	} {
		if got := probeReachable(tc.err); got != tc.want {
			t.Errorf("probeReachable(%v)=%v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestPrivateRootTunnelProbe(t *testing.T) {
	srv := tlsProbeServer(t, false)
	probeRoots = x509.NewCertPool()
	defer func() { probeRoots = nil }()
	var d net.Dialer
	tt := newTestTunnel(t, ModeAuto, d.DialContext, d.DialContext)
	tt.established = func() bool { return true }
	tt.probeTunnel = func(ctx context.Context, _, _ string) error {
		addr := srv.Listener.Addr().String()
		host, _, _ := net.SplitHostPort(addr)
		return probeTLSVia(ctx, d.DialContext, addr, host)
	}
	g := tt.testGroup("relay.example.org:993")
	res := tt.confirmViaTunnel(g, probeResult{Relays: 1, NetworkOK: true, hosts: []string{"relay.example.org:993"}})
	if !res.TunnelChecked || !res.TunnelOK {
		t.Fatalf("private-root tunnel probe did not answer: %+v", res)
	}
}

func TestPinnedTunnelToAutoRecoversWithPrivateRoot(t *testing.T) {
	resetKnownStrategies()
	defer resetKnownStrategies()
	srv := tlsProbeServer(t, false)
	probeRoots = x509.NewCertPool()
	defer func() { probeRoots = nil }()
	addr := srv.Listener.Addr().String()
	var d net.Dialer
	tt := newTestTunnel(t, ModeAlways, d.DialContext, d.DialContext)
	tt.cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	tt.client = &routePool{clients: []soarClient{&poolFixture{dial: d.DialContext}}, ids: []string{"test"}, kinds: []string{"http"}, ipv6: []bool{false}}
	tt.probeTunnel = func(context.Context, string, string) error { return nil }
	g := tt.testGroup("relay.example.org:993")
	tt.probeTLS = func(ctx context.Context, _, _ string) error {
		host, _, _ := net.SplitHostPort(addr)
		return probeTLS(ctx, addr, host)
	}
	if err := tt.SetMode("route:test"); err != nil {
		t.Fatal(err)
	}
	if g.route != RouteTunnel || !previousNetwork(tt.cfg).Groups[groupKey(g.name)] {
		t.Fatal("pinned route did not save tunnel use")
	}
	if err := tt.SetMode(ModeAuto); err != nil {
		t.Fatal(err)
	}
	inherited := slices.ContainsFunc(tt.log.last(30), func(e Event) bool {
		return strings.Contains(e.Msg, "previous run used the tunnel")
	})
	if g.route != RouteTunnel || !inherited {
		t.Fatal("Auto did not inherit the previous run's tunnel")
	}
	for range recoveredAfter {
		tt.check()
	}
	s := tt.Status(0).Groups[g.name]
	if s.Route != "direct" || s.State != "direct" || s.LastResult.RelayOK != 1 {
		t.Fatalf("private-root relay did not recover: %+v", s)
	}
}
