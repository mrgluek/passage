package tunnel

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Forward carries plain TCP streams to a fixed destination through the router.
type Forward struct {
	Name   string `json:"name"`
	Listen string `json:"listen"`
	Target string `json:"target"`
}

// ForwardStatus describes one local TCP listener. Conns counts accepted streams.
type ForwardStatus struct {
	Name     string     `json:"name"`
	Listen   string     `json:"listen"`
	Target   string     `json:"target"`
	Up       bool       `json:"up"`
	Conns    int64      `json:"conns"`
	LastConn *time.Time `json:"last_conn,omitempty"`
	Error    string     `json:"error,omitempty"`
}

type forwardListener struct {
	ln     net.Listener
	status ForwardStatus
}

func forwardPort(port string) (int, bool) {
	for _, c := range port {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(port)
	return n, err == nil && n > 0 && n <= 65535
}

func forwardHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validateForwards(cfg Config) error {
	if len(cfg.Forwards) > 8 {
		return fmt.Errorf("at most 8 forwards are allowed")
	}
	socksHost, socksPort, _ := net.SplitHostPort(cfg.Listen)
	socksNumber, _ := strconv.Atoi(socksPort)
	seen := map[int]bool{}
	for i, f := range cfg.Forwards {
		host, port, err := net.SplitHostPort(f.Listen)
		n, valid := forwardPort(port)
		if err != nil || host != "127.0.0.1" || !valid {
			return fmt.Errorf("forward %d: listen must be 127.0.0.1:<port> with port 1-65535", i+1)
		}
		if n == socksNumber && (socksHost == "127.0.0.1" || socksHost == "localhost" || socksHost == "" || socksHost == "0.0.0.0" || socksHost == "::") {
			return fmt.Errorf("forward %d: listen must differ from the SOCKS listener", i+1)
		}
		if seen[n] {
			return fmt.Errorf("forward %d: duplicate listen address", i+1)
		}
		seen[n] = true
		host, port, err = net.SplitHostPort(f.Target)
		if _, valid := forwardPort(port); err != nil || !valid || !forwardHost(host) {
			return fmt.Errorf("forward %d: target must be a host:port with port 1-65535", i+1)
		}
	}
	return nil
}

func (t *Tunnel) startForwards() {
	t.forwards = make([]forwardListener, len(t.cfg.Forwards))
	for i, f := range t.cfg.Forwards {
		ln, _, err := listenProxy(f.Listen, time.Second)
		status := ForwardStatus{Name: f.Name, Listen: f.Listen, Target: f.Target, Up: err == nil}
		if err != nil {
			// Keep addresses and OS-specific wrappers out of the status error.
			if op, ok := err.(*net.OpError); ok {
				err = op.Err
			}
			status.Error = err.Error()
			t.log.add("forward %d: listen failed: %s", i+1, status.Error)
		} else {
			t.log.add("forward %d: listening on %s", i+1, f.Listen)
		}
		t.forwards[i] = forwardListener{ln: ln, status: status}
	}
	for i, f := range t.forwards {
		if f.ln != nil {
			t.wg.Add(1)
			go t.serveListener(f.ln, i)
		}
	}
}
