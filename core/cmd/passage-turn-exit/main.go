// Command passage-turn-exit serves encrypted streams over UDP.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

type rule struct {
	Host  string   `json:"host,omitempty"`
	CIDR  string   `json:"cidr,omitempty"`
	Ports []uint16 `json:"ports"`
}
type config struct {
	Key     string `json:"key"`
	Allowed []rule `json:"allowed"`
}

func public(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}
func policy(rules []rule) (func(string, uint16) (net.Conn, byte), error) {
	if len(rules) == 0 || len(rules) > 256 {
		return nil, errors.New("configure allowed destinations")
	}
	for _, r := range rules {
		if (r.Host == "") == (r.CIDR == "") || len(r.Ports) == 0 {
			return nil, errors.New("invalid destination rule")
		}
		if r.CIDR != "" {
			p, err := netip.ParsePrefix(r.CIDR)
			if err != nil || p.Bits() < 16 || p.Addr().Is6() && p.Bits() < 32 {
				return nil, errors.New("invalid destination range")
			}
		}
		for _, p := range r.Ports {
			if p == 0 || p == 25 {
				return nil, errors.New("invalid destination port")
			}
		}
	}
	return func(host string, port uint16) (net.Conn, byte) {
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		ip, _ := netip.ParseAddr(host)
		allowed := false
		for _, r := range rules {
			name := strings.TrimSuffix(strings.ToLower(r.Host), ".")
			match := name != "" && host == name
			if strings.HasPrefix(name, ".") {
				match = host == name[1:] || strings.HasSuffix(host, name)
			}
			if r.CIDR != "" {
				p, _ := netip.ParsePrefix(r.CIDR)
				match = ip.IsValid() && p.Contains(ip)
			}
			if match {
				for _, p := range r.Ports {
					allowed = allowed || p == port
				}
			}
		}
		if !allowed {
			return nil, 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, 2
		}
		// Resolve once and dial only a checked literal to prevent DNS rebinding.
		for _, addr := range ips {
			if !public(addr) {
				return nil, 1
			}
		}
		for _, addr := range ips {
			conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), strconv.Itoa(int(port))))
			if err == nil {
				return conn, 0
			}
		}
		return nil, 2
	}, nil
}
func run() error {
	configFile := flag.String("config", "", "private JSON configuration file")
	listen := flag.String("listen", "0.0.0.0:34780", "UDP listen address")
	flag.Parse()
	data, err := os.ReadFile(*configFile)
	if err != nil {
		return errors.New("cannot read configuration")
	}
	var cfg config
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return errors.New("invalid configuration")
	}
	key, err := base64.StdEncoding.DecodeString(cfg.Key)
	if err != nil {
		return errors.New("invalid key")
	}
	dial, err := policy(cfg.Allowed)
	if err != nil {
		return err
	}
	exit, err := tunnel.NewTURNExit(key, dial)
	if err != nil {
		return err
	}
	defer exit.Close()
	conn, err := net.ListenPacket("udp", *listen)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); exit.Close(); conn.Close() }()
	err = exit.Serve(conn)
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "passage-turn-exit:", err)
		os.Exit(1)
	}
}
