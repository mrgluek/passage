package tunnel

import (
	"context"
	"net"
	"sync"
	"syscall"
	"time"

	"golang.getoutline.org/sdk/transport"
	"golang.getoutline.org/sdk/transport/split"
	"golang.getoutline.org/sdk/transport/tlsfrag"
)

// strategy is one way to send a connection's first bytes on the smart route.
type strategy struct {
	name string
	dial dialFunc
}

// smartStrategies lists the strategies in the order they are tried.
func smartStrategies() []strategy {
	base := &transport.TCPDialer{Dialer: net.Dialer{Timeout: directDialTimeout}}
	out := []strategy{}
	if d, err := tlsfrag.NewFixedLenStreamDialer(base, 1); err == nil {
		out = append(out, strategy{"tlsfrag:1", streamDial(d)})
	}
	if d, err := split.NewStreamDialer(base, split.NewFixedSplitIterator(2)); err == nil {
		out = append(out, strategy{"split:2", streamDial(d)})
	}
	out = append(out, strategy{"disorder:1", disorderDial(1)})
	return out
}

func streamDial(d transport.StreamDialer) dialFunc {
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		return d.DialStream(ctx, addr)
	}
}

// disorderDial sends the first n bytes with a TTL of 1, so they die at the
// first hop and the kernel retransmits them after the rest.
func disorderDial(n int) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: directDialTimeout}
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tc, ok := c.(*net.TCPConn)
		if !ok {
			return c, nil
		}
		return &disorderConn{TCPConn: tc, n: n}, nil
	}
}

type disorderConn struct {
	*net.TCPConn
	once sync.Once
	n    int
}

func (c *disorderConn) Write(b []byte) (int, error) {
	if len(b) <= c.n {
		return c.TCPConn.Write(b)
	}
	first := false
	c.once.Do(func() { first = true })
	if !first {
		return c.TCPConn.Write(b)
	}
	restore, err := c.lowTTL()
	if err != nil {
		return c.TCPConn.Write(b)
	}
	n, err := c.TCPConn.Write(b[:c.n])
	restore()
	if err != nil {
		return n, err
	}
	m, err := c.TCPConn.Write(b[c.n:])
	return n + m, err
}

// lowTTL sets the socket's TTL (hop limit) to 1 and returns a func that
// puts the old value back.
func (c *disorderConn) lowTTL() (func(), error) {
	raw, err := c.TCPConn.SyscallConn()
	if err != nil {
		return nil, err
	}
	level, opt := syscall.IPPROTO_IP, syscall.IP_TTL
	if a, ok := c.TCPConn.RemoteAddr().(*net.TCPAddr); ok && a.IP.To4() == nil {
		level, opt = syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS
	}
	old := 64
	var serr error
	if err := raw.Control(func(fd uintptr) {
		if v, err := syscall.GetsockoptInt(int(fd), level, opt); err == nil && v > 0 {
			old = v
		}
		serr = syscall.SetsockoptInt(int(fd), level, opt, 1)
	}); err != nil {
		return nil, err
	}
	if serr != nil {
		return nil, serr
	}
	return func() {
		raw.Control(func(fd uintptr) { syscall.SetsockoptInt(int(fd), level, opt, old) })
	}, nil
}

// findStrategy returns the first of list that completes a TLS handshake
// with every host, or nil. Each try gets probeTimeout.
func (t *Tunnel) findStrategy(list []strategy, hosts []string) *strategy {
	for _, s := range list {
		ok := true
		for _, hp := range hosts {
			host, _, _ := net.SplitHostPort(hp)
			ctx, cancel := context.WithTimeout(t.ctx, probeTimeout)
			err := probeTLSVia(ctx, s.dial, hp, host)
			cancel()
			if err != nil {
				ok = false
				break
			}
		}
		if ok {
			return &s
		}
		if t.ctx.Err() != nil {
			return nil
		}
	}
	return nil
}

// smartCheck runs while the probe hosts fail directly: it keeps the current
// strategy if it still works, otherwise looks for one. It reports whether
// the smart route works and sets it up (or tears it down) to match.
func (t *Tunnel) smartCheck(hosts []string) bool {
	t.mu.Lock()
	cur := t.strategy
	t.mu.Unlock()
	if cur != nil && t.findStrategy([]strategy{*cur}, hosts) != nil {
		return true
	}
	start := time.Now()
	found := t.findStrategy(t.strategies, hosts)
	t.mu.Lock()
	defer t.mu.Unlock()
	if found == nil {
		if t.strategy != nil {
			t.log.add("smart route: %s stopped working, none found", t.strategy.name)
		}
		t.strategy = nil
		t.ways[RouteSmart] = way{}
		return false
	}
	if t.strategy == nil || t.strategy.name != found.name {
		t.log.add("smart route: %s works (search took %v)", found.name, time.Since(start).Round(100*time.Millisecond))
	}
	t.strategy = found
	t.ways[RouteSmart] = way{found.dial, directDialTimeout}
	return true
}
