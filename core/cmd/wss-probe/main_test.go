package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
	"github.com/mrgluek/passage/core/tunnel"
)

func TestHTTPProbeEchoAndTLSValidation(t *testing.T) {
	payload := bytes.Repeat([]byte("data"), 65536)
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer destination.Close()
	key := bytes.Repeat([]byte{7}, 32)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	exit, err := tunnel.NewHTTPExit(key, "/probe", func(host string, port uint16) (net.Conn, byte) {
		address := destination.Listener.Addr().String()
		if host == "echo.example.org" {
			address = ln.Addr().String()
		} else if host != "example.com" {
			return nil, 1
		}
		if port != 443 {
			return nil, 1
		}
		conn, err := net.Dial("tcp", address)
		if err != nil {
			return nil, 2
		}
		return conn, 0
	})
	if err != nil {
		t.Fatal(err)
	}
	defer exit.Close()
	origin := httptest.NewTLSServer(exit)
	defer origin.Close()
	original := http.DefaultTransport
	http.DefaultTransport = origin.Client().Transport.(*http.Transport).Clone()
	defer func() { http.DefaultTransport = original }()

	route := links.Route{ID: "web", Type: "http", Wire: "http-v1", Params: map[string]any{"endpoint": origin.URL + "/probe", "key": key}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o := options{Target: "echo.example.org:443", Protocol: "echo"}
	if _, err := probeRoute(ctx, route, o); err != nil {
		t.Fatal("HTTP echo probe:", err)
	}
	o.Target, o.Protocol = "example.com:443", "tls"
	if _, err := probeRoute(ctx, route, o); err == nil {
		t.Fatal("untrusted destination TLS passed")
	}
	o.Protocol, o.Path = "download", "/"
	if _, err := probeRoute(ctx, route, o); err == nil {
		t.Fatal("untrusted download TLS passed")
	}

	route.Params["key"] = bytes.Repeat([]byte{8}, 32)
	o.Target, o.Protocol, o.Path = "echo.example.org:443", "echo", ""
	if _, err := probeRoute(ctx, route, o); err == nil {
		t.Fatal("wrong HTTP key passed")
	}
}

func TestProbeRejectsUnsupportedRoute(t *testing.T) {
	if _, err := probeRoute(context.Background(), links.Route{Type: "soar"}, options{}); err == nil {
		t.Fatal("unsupported route accepted")
	}
}
