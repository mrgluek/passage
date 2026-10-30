package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.getoutline.org/sdk/transport"
	"golang.getoutline.org/sdk/transport/split"
	"golang.getoutline.org/sdk/transport/tlsfrag"
)

// knownStrategies remembers, per network, the strategy that worked last, in
// memory and, when Config.StateFile is set, in that file, so it survives the
// app being stopped. Networks are kept as hashes; at most maxKnownNetworks.
var knownStrategies = struct {
	sync.Mutex
	m      map[string]knownStrategy
	loaded map[string]bool // state files already read
}{m: map[string]knownStrategy{}, loaded: map[string]bool{}}

type knownStrategy struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

const maxKnownNetworks = 32

// networkKey identifies the network by what the app passes in at start.
func networkKey(c Config) string {
	sum := sha256.Sum256([]byte(strings.Join(c.SystemResolvers, ",") + "|" + c.Operator))
	return hex.EncodeToString(sum[:12])
}

// knownStrategyName returns the strategy remembered for c's network.
func knownStrategyName(c Config) string {
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	return knownStrategies.m[networkKey(c)].Name
}

// rememberStrategy records name for c's network ("" forgets it) and saves.
func rememberStrategy(c Config, name string) {
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	k := networkKey(c)
	if old, ok := knownStrategies.m[k]; ok && old.Name == name && time.Since(old.At) < time.Hour {
		return // unchanged and recent: no write
	}
	if name == "" {
		delete(knownStrategies.m, k)
	} else {
		knownStrategies.m[k] = knownStrategy{name, time.Now().UTC()}
	}
	for len(knownStrategies.m) > maxKnownNetworks {
		oldest := ""
		for key, v := range knownStrategies.m {
			if oldest == "" || v.At.Before(knownStrategies.m[oldest].At) {
				oldest = key
			}
		}
		delete(knownStrategies.m, oldest)
	}
	saveKnownLocked(c.StateFile)
}

func loadKnownLocked(path string) {
	if path == "" || knownStrategies.loaded[path] {
		return
	}
	knownStrategies.loaded[path] = true
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f struct {
		Strategies map[string]knownStrategy `json:"strategies"`
	}
	if json.Unmarshal(b, &f) != nil {
		return
	}
	for k, v := range f.Strategies {
		if cur, ok := knownStrategies.m[k]; !ok || v.At.After(cur.At) {
			knownStrategies.m[k] = v
		}
	}
}

func saveKnownLocked(path string) {
	if path == "" {
		return
	}
	b, err := json.Marshal(map[string]any{"strategies": knownStrategies.m})
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, path)
	}
}

// useKnownStrategy sets up the smart route with the strategy that worked on
// this network before, if any. Called at start, before the monitor runs.
func (t *Tunnel) useKnownStrategy() {
	name := knownStrategyName(t.cfg)
	for _, s := range t.strategies {
		if s.name == name {
			t.strategy = &s
			t.ways[RouteSmart] = way{s.dial, directDialTimeout}
			t.log.add("smart route: %s worked on this network before", name)
			return
		}
	}
}

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
			probe := t.smartProbe
			if probe == nil {
				probe = probeTLSVia
			}
			err := probe(ctx, s.dial, hp, host)
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
		rememberStrategy(t.cfg, cur.name)
		return true
	}
	start := time.Now()
	found := t.findStrategy(t.strategies, hosts)
	t.mu.Lock()
	defer t.mu.Unlock()
	if found == nil {
		rememberStrategy(t.cfg, "")
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
	rememberStrategy(t.cfg, found.name)
	return true
}
