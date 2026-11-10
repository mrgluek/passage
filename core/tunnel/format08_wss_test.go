package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

func TestWSSV2DialIPAndSNI(t *testing.T) {
	_, exit := wssFixture(t, 4, func(c net.Conn) { io.Copy(c, c) })
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS.ServerName != "example.com" || !strings.HasPrefix(r.Host, "endpoint.example.org:") {
			t.Errorf("SNI %q, Host %q", r.TLS.ServerName, r.Host)
		}
		exit.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ip, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	for _, sni := range []string{"example.com", "wrong.example.org", ""} {
		t.Run("sni="+sni, func(t *testing.T) {
			params := links.WSSParams{Endpoint: "wss://endpoint.example.org:" + port + "/passage", Key: wssTestKey, DialIP: ip, SNI: sni}
			client, err := NewWSSClient(params)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			tr := client.httpClient.Transport.(*http.Transport)
			if tr.TLSClientConfig == nil {
				tr.TLSClientConfig = &tls.Config{}
			}
			tr.TLSClientConfig.RootCAs = x509.NewCertPool()
			tr.TLSClientConfig.RootCAs.AddCert(server.Certificate())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, "tcp", "example.org:443")
			if sni != "example.com" {
				if err == nil {
					conn.Close()
					t.Fatal("certificate hostname verification missing")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err = conn.Write([]byte("hello")); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, 5)
			if _, err = io.ReadFull(conn, got); err != nil || string(got) != "hello" {
				t.Fatal("WSS round trip", err)
			}
			first := wssServer(params, client)
			if first.addr != server.Listener.Addr().String() || first.name != sni || !first.tls {
				t.Fatal("diagnostic endpoint overrides missing")
			}
		})
	}
}

func TestWSSWireValidationAndDefaults(t *testing.T) {
	for _, wire := range []string{"wss-v1", "wss-v2"} {
		for _, overrides := range []bool{false, true} {
			r := subRoute("one")
			r.Wire = wire
			if overrides {
				r.Params["dial_ip"] = ""
				r.Params["sni"] = ""
			}
			cfg, err := (Config{Routes: []links.Route{r}}).Normalize()
			if wire == "wss-v1" && overrides {
				if err == nil {
					t.Fatal("frozen v1 accepted overrides")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			pool, err := newRoutePool(cfg)
			if err != nil {
				t.Fatal(err)
			}
			p := pool.(*routePool)
			client := p.clients[0].(*WSSClient)
			tr := client.httpClient.Transport.(*http.Transport)
			if client.params.DialIP != "" || client.params.SNI != "" || tr.TLSClientConfig != nil && tr.TLSClientConfig.ServerName != "" {
				t.Fatal("default WSS settings changed")
			}
			pool.Close()
		}
	}
}
