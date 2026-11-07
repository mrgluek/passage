package tunnel

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync/atomic"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

// Each stage of a route check has its own limit; the stages before the data
// stage together fit in the app's network test.
const (
	checkDNSTimeout = 3 * time.Second
	checkTCPTimeout = 5 * time.Second
	checkTLSTimeout = 5 * time.Second
	checkAppTimeout = 5 * time.Second
	checkMaxAddrs   = 4
	checkDataLimit  = 256 << 10
	// A route that can retry on a fresh session keeps time for it.
	checkDataRetryTimeout = 8 * time.Second
)

// RouteCheck is one route tested stage by stage. It never names the route's
// server.
type RouteCheck struct {
	Route  string       `json:"route"`
	Stages []RouteStage `json:"stages"`
}

// RouteStage is one stage: dns, tcp, tls, app, data or fresh. The stages up
// to app connect directly and end at the first failure; data goes through
// the route itself, so it runs either way. fresh repeats a failed data stage
// on a new session, where the route has sessions.
type RouteStage struct {
	Stage string `json:"stage"`
	OK    bool   `json:"ok"`
	MS    int64  `json:"ms"`
	Bytes int64  `json:"bytes,omitempty"` // data only, also when it broke off
	Error string `json:"error,omitempty"`
	// Data only: the last target tried, and how many before it were refused.
	Target  string `json:"target,omitempty"`
	Refused int    `json:"refused,omitempty"`
	// Received is what the route's sessions had read from the network when
	// data failed.
	Received int64 `json:"received,omitempty"`
}

// routeServer is where a route connects first. A route without one, like
// soar, has no staged check.
type routeServer struct {
	addr string // host:port
	name string // TLS name when addr has none of its own
	tls  bool
	app  func(context.Context) error // nil: nothing to check above TLS
	// fresh, when set, gives a dial on a new session and a function to end it.
	fresh    func() (dialFunc, func(), error)
	received func() int64
}

func wssServer(p links.WSSParams, client *WSSClient) routeServer {
	return sessionServer(urlServer(p.Endpoint), client, func() (*WSSClient, error) { return NewWSSClient(p) })
}

func turnServer(p links.TURNParams, client *WSSClient) routeServer {
	s := routeServer{}
	if u, err := links.TURNURL(p.Relay); err == nil && (u.Scheme == "turns" || u.Query().Get("transport") == "tcp") {
		port := u.Port()
		if port == "" {
			port = "3478"
			if u.Scheme == "turns" {
				port = "443"
			}
		}
		s.addr = net.JoinHostPort(cmp.Or(p.DialIP, u.Hostname()), port)
		s.name = cmp.Or(p.SNI, u.Hostname())
		s.tls = u.Scheme == "turns"
	}
	return sessionServer(s, client, func() (*WSSClient, error) { return NewTURNClient(p) })
}

func httpServer(p links.HTTPParams, client *WSSClient) routeServer {
	s := urlServer(p.Endpoint)
	if host, port, err := net.SplitHostPort(s.addr); err == nil && p.DialIP != "" {
		s.addr, s.name = net.JoinHostPort(p.DialIP, port), host
	}
	return sessionServer(s, client, func() (*WSSClient, error) { return NewHTTPClient(p) })
}

func sessionServer(s routeServer, client *WSSClient, open func() (*WSSClient, error)) routeServer {
	s.app = func(ctx context.Context) error { return probeSession(ctx, open) }
	s.received = client.received
	s.fresh = func() (dialFunc, func(), error) {
		c, err := open()
		if err != nil {
			return nil, nil, err
		}
		return c.DialContext, func() { c.Close() }, nil
	}
	return s
}

func urlServer(raw string) routeServer {
	u, err := url.Parse(raw)
	if err != nil {
		return routeServer{}
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return routeServer{addr: net.JoinHostPort(u.Hostname(), port), tls: true}
}

func probeSession(ctx context.Context, open func() (*WSSClient, error)) error {
	client, err := open()
	if err != nil {
		return err
	}
	defer client.Close()
	s, err := client.dialSession(ctx)
	if err != nil {
		return err
	}
	s.fail(net.ErrClosed)
	return nil
}

// dataTarget is somewhere the data stage can reach through a route.
type dataTarget struct {
	addr     string
	telegram bool
}

// checkRoute runs the direct stages against server, then the data stage
// through dial. An exit may refuse a target, which says nothing about the
// route, so the stage moves on to the next one.
func checkRoute(ctx context.Context, name string, server routeServer, dial dialFunc, targets []dataTarget) RouteCheck {
	c := RouteCheck{Route: name, Stages: checkServer(ctx, server)}
	if len(targets) == 0 {
		return c
	}
	dataCtx, cancel := ctx, func() {}
	if server.fresh != nil {
		dataCtx, cancel = context.WithTimeout(ctx, checkDataRetryTimeout)
	}
	defer cancel()
	s, rest := probeTargets(dataCtx, dial, targets, "data")
	if s.OK || errors.Is(s.err, errDestinationRefused) || server.fresh == nil {
		c.Stages = append(c.Stages, s.RouteStage)
		return c
	}
	if server.received != nil {
		s.Received = server.received()
	}
	c.Stages = append(c.Stages, s.RouteStage)
	freshDial, done, err := server.fresh()
	if err != nil {
		c.Stages = append(c.Stages, newStage("fresh", time.Now(), err))
		return c
	}
	defer done()
	f, _ := probeTargets(ctx, freshDial, rest, "fresh")
	c.Stages = append(c.Stages, f.RouteStage)
	return c
}

// targetsResult is a data stage and the error behind it.
type targetsResult struct {
	RouteStage
	err error
}

// probeTargets tries the targets until one is not refused and returns the
// stage with the targets from that one on.
func probeTargets(ctx context.Context, dial dialFunc, targets []dataTarget, stage string) (targetsResult, []dataTarget) {
	start := time.Now()
	var n int64
	var err error
	at := 0
	for i, t := range targets {
		at = i
		if n, err = probeTarget(ctx, dial, t); !errors.Is(err, errDestinationRefused) {
			break
		}
	}
	s := newStage(stage, start, err)
	s.Bytes, s.Target, s.Refused = n, targets[at].addr, at
	return targetsResult{s, err}, targets[at:]
}

func probeTarget(ctx context.Context, dial dialFunc, t dataTarget) (int64, error) {
	if !t.telegram {
		return probeDataVia(ctx, dial, t.addr)
	}
	var conn *countingConn
	err := probeMTProtoVia(ctx, func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		conn = &countingConn{Conn: c}
		return conn, nil
	}, t.addr)
	if conn == nil {
		return 0, err
	}
	return conn.n.Load(), err
}

func newStage(stage string, start time.Time, err error) RouteStage {
	s := RouteStage{Stage: stage, OK: err == nil, MS: time.Since(start).Milliseconds()}
	if err != nil {
		s.Error = shortError(err)
	}
	return s
}

func checkServer(ctx context.Context, server routeServer) []RouteStage {
	var stages []RouteStage
	// step runs one stage and says whether the next may run.
	step := func(stage string, limit time.Duration, f func(context.Context) error) bool {
		cctx, cancel := context.WithTimeout(ctx, limit)
		defer cancel()
		start := time.Now()
		err := f(cctx)
		stages = append(stages, newStage(stage, start, err))
		return err == nil
	}
	host, port, err := net.SplitHostPort(server.addr)
	if err != nil {
		if server.addr == "" && server.app != nil {
			step("app", checkAppTimeout, server.app)
			return stages
		}
		return nil
	}
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		ok := step("dns", checkDNSTimeout, func(c context.Context) (err error) {
			ips, err = net.DefaultResolver.LookupIP(c, "ip", host)
			return err
		})
		if !ok {
			return stages
		}
	}
	var conn net.Conn
	ok := step("tcp", checkTCPTimeout, func(c context.Context) (err error) {
		conn, err = dialFirst(c, ips, port)
		return err
	})
	if !ok {
		return stages
	}
	defer conn.Close()
	if server.tls {
		ok = step("tls", checkTLSTimeout, func(c context.Context) error {
			return tls.Client(conn, &tls.Config{ServerName: cmp.Or(server.name, host), MinVersion: tls.VersionTLS12, RootCAs: probeRoots}).HandshakeContext(c)
		})
		if !ok {
			return stages
		}
	}
	if server.app != nil {
		step("app", checkAppTimeout, server.app)
	}
	return stages
}

// dialFirst tries the addresses in turn, sharing the time so one that never
// answers leaves room for the next.
func dialFirst(ctx context.Context, ips []net.IP, port string) (net.Conn, error) {
	if len(ips) > checkMaxAddrs {
		ips = ips[:checkMaxAddrs]
	}
	var last error
	for i, ip := range ips {
		limit := checkTCPTimeout / time.Duration(len(ips))
		if i == len(ips)-1 {
			limit = checkTCPTimeout
		}
		c, cancel := context.WithTimeout(ctx, limit)
		conn, err := new(net.Dialer).DialContext(c, "tcp", net.JoinHostPort(ip.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

// countingConn counts the bytes read, TLS records included.
type countingConn struct {
	net.Conn
	n atomic.Int64
}

func countingDial(dial dialFunc) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &countingConn{Conn: c}, nil
	}
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.n.Add(int64(n))
	return n, err
}

// probeDataVia does a TLS handshake with target through dial and, on port
// 443, fetches a page. It returns the bytes received, also on failure.
func probeDataVia(ctx context.Context, dial dialFunc, target string) (int64, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return 0, err
	}
	raw, err := dial(ctx, "tcp", target)
	if err != nil {
		return 0, err
	}
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	conn := &countingConn{Conn: raw}
	secure := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: probeRoots})
	if err = secure.HandshakeContext(ctx); err == nil && port == "443" {
		if _, err = fmt.Fprintf(secure, "GET / HTTP/1.1\r\nHost: %s\r\nUser-Agent: curl/8.7.1\r\nAccept: */*\r\nConnection: close\r\n\r\n", host); err == nil {
			_, err = io.CopyN(io.Discard, secure, checkDataLimit)
			if errors.Is(err, io.EOF) {
				err = nil
			}
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return conn.n.Load(), err
}
