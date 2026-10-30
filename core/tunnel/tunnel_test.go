package tunnel

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

func TestParseLink(t *testing.T) {
	urlKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)) // has '-'/'_'
	cfg, err := ParseLink("passage://tunnel?v=1&zone=T.Example.ORG.&key=" + urlKey + "&r=1.2.3.4:53,1.2.3.4:53,%5B2001:db8::1%5D:853,9.9.9.9&c=example.com:443,example.net")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Zone != "t.example.org" {
		t.Errorf("zone %q", cfg.Zone)
	}
	if want := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)); cfg.Key != want {
		t.Errorf("key not normalized: %q", cfg.Key)
	}
	if got := strings.Join(cfg.Resolvers, ","); got != "1.2.3.4:53,[2001:db8::1]:853,9.9.9.9:53" {
		t.Errorf("resolvers %q", got)
	}
	if strings.Join(cfg.Canaries, ",") != "example.com:443,example.net:443" || cfg.Mode != ModeAuto || cfg.Listen != "127.0.0.1:0" {
		t.Errorf("defaults: %+v", cfg)
	}

	cfg, err = ParseLink("passage://tunnel?zone=t.example.org&key=" + testKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Resolvers) != 0 || cfg.Canaries != nil {
		t.Errorf("a link without r/c must not freeze today's defaults: %+v", cfg)
	}
	if n, _ := cfg.Normalize(); len(n.Effective()) != len(DefaultResolvers) || len(n.Canaries) != len(DefaultCanaries) {
		t.Errorf("defaults not applied at start: %+v", n)
	}

	for _, scheme := range []string{"velta", "PASSAGE"} {
		if _, err := ParseLink(scheme + "://tunnel?zone=t.example.org&key=" + testKey); err != nil {
			t.Errorf("%s:// link: %v", scheme, err)
		}
	}

	for _, bad := range []string{
		"https://tunnel?zone=t.example.org&key=" + testKey,
		"passage://chat?zone=t.example.org&key=" + testKey,
		"passage://tunnel?v=2&zone=t.example.org&key=" + testKey,
		"passage://tunnel?zone=example&key=" + testKey,
		"passage://tunnel?zone=bad_label.example.org&key=" + testKey,
		"passage://tunnel?zone=t.example.org&key=c2hvcnQ=",
		"passage://tunnel?zone=t.example.org&key=" + testKey + "&r=dns.example.org:53",
		"passage://tunnel?zone=t.example.org&key=" + testKey + "&r=1.2.3.4:0",
	} {
		if _, err := ParseLink(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestDecide(t *testing.T) {
	blocked := probeResult{Relays: 2, CanaryOK: true}
	ok := probeResult{Relays: 2, RelayOK: 1, CanaryOK: true}
	offline := probeResult{Relays: 2}

	r, f, _, s := decide(RouteDirect, blocked, 0, 0, true)
	if r != RouteDirect || f != 1 || s != "blocked" {
		t.Fatalf("one blocked probe must not switch yet: %v %d %s", r, f, s)
	}
	r, f, _, _ = decide(r, blocked, f, 0, true)
	if r != RouteTunnel {
		t.Fatalf("second blocked probe must switch to the tunnel")
	}
	r, f, _, s = decide(r, offline, f, 0, true)
	if r != RouteTunnel || s != "offline" {
		t.Fatalf("offline must not change the route: %v %s", r, s)
	}
	k := 0
	for i := 1; i <= recoveredAfter; i++ {
		r, f, k, _ = decide(r, ok, f, k, true)
		if i < recoveredAfter && r != RouteTunnel {
			t.Fatalf("switched back after %d ok probes", i)
		}
	}
	if r != RouteDirect || f != 0 {
		t.Fatalf("must return to direct after %d ok probes", recoveredAfter)
	}
	if r2, _, _, s := decide(RouteDirect, probeResult{}, 0, 0, true); r2 != RouteDirect || s != "unknown" {
		t.Fatalf("no known relays must change nothing")
	}
}

// newTestTunnel builds a Tunnel without a soar client, with fake dialers.
func newTestTunnel(t *testing.T, mode string, direct, tun dialFunc) *Tunnel {
	t.Helper()
	cfg, err := Config{Zone: "t.example.org", Key: testKey, Mode: mode, User: "u", Pass: "p"}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	tt := &Tunnel{cfg: cfg, log: newEventLog(100), kick: make(chan struct{}, 1), mode: mode,
		conns: map[*tracked]struct{}{}, relays: map[string]time.Time{}, relayPorts: map[string]string{}, state: "unknown",
		ways:     [numRoutes]way{RouteDirect: {direct, time.Second}, RouteTunnel: {tun, time.Second}},
		probeTLS: func(context.Context, string, string) error { return errors.New("no probes in tests") }}
	tt.ctx, tt.cancel = context.WithCancel(context.Background())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tt.ln = ln
	tt.setRouteLocked(initialRoute(mode), "start")
	tt.wg.Add(1)
	go tt.serve()
	t.Cleanup(tt.Close)
	return tt
}

// echoDialer returns one end of a pipe whose far end echoes with a prefix.
func echoDialer(prefix string, calls *atomic.Int32) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls.Add(1)
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			buf := make([]byte, 64)
			n, _ := b.Read(buf)
			b.Write(append([]byte(prefix), buf[:n]...))
		}()
		return a, nil
	}
}

func failDialer(calls *atomic.Int32) dialFunc {
	return func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("blocked")
	}
}

// socksConnect performs a client-side SOCKS5 handshake with user/pass auth.
func socksConnect(t *testing.T, addr, user, pass, target string) (net.Conn, byte) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte{5, 1, 2})
	r := make([]byte, 2)
	io.ReadFull(c, r)
	if r[1] != 2 {
		t.Fatalf("server chose method %d", r[1])
	}
	c.Write(append(append(append([]byte{1, byte(len(user))}, user...), byte(len(pass))), pass...))
	io.ReadFull(c, r)
	if r[1] != 0 {
		return c, 0xff
	}
	host, port, _ := net.SplitHostPort(target)
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	var p int
	for _, ch := range port {
		p = p*10 + int(ch-'0')
	}
	req = append(req, byte(p>>8), byte(p))
	c.Write(req)
	rep := make([]byte, 10)
	if _, err := io.ReadFull(c, rep); err != nil {
		t.Fatal(err)
	}
	return c, rep[1]
}

func roundTrip(t *testing.T, c net.Conn) string {
	t.Helper()
	c.Write([]byte("hi"))
	b, _ := io.ReadAll(c)
	return string(b)
}

func TestAuthRequired(t *testing.T) {
	var d atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &d), echoDialer("T:", &d))
	c, code := socksConnect(t, tt.Addr(), "u", "wrong", "relay.example.org:993")
	defer c.Close()
	if code != 0xff || d.Load() != 0 {
		t.Fatalf("bad password must be refused before dialing (code %d, dials %d)", code, d.Load())
	}
}

func TestDirectThenFallback(t *testing.T) {
	var direct, tun atomic.Int32
	var blocked atomic.Bool // the dialer is read from server goroutines: flip a flag, not the field
	works, fails := echoDialer("D:", &direct), failDialer(&direct)
	switchable := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if blocked.Load() {
			return fails(ctx, network, addr)
		}
		return works(ctx, network, addr)
	}
	tt := newTestTunnel(t, ModeAuto, switchable, echoDialer("T:", &tun))
	c, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	if code != repSucceeded {
		t.Fatalf("connect failed: %d", code)
	}
	if got := roundTrip(t, c); got != "D:hi" {
		t.Fatalf("direct route: got %q", got)
	}
	c.Close()

	blocked.Store(true)
	c, code = socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	if code != repSucceeded {
		t.Fatalf("fallback connect failed: %d", code)
	}
	if got := roundTrip(t, c); got != "T:hi" {
		t.Fatalf("auto mode must fall back to the tunnel: got %q", got)
	}
	c.Close()
	if s := tt.Status(10); len(s.Relays) != 1 || s.Relays[0] != "relay.example.org" {
		t.Fatalf("relay host not learned: %v", s.Relays)
	}
}

func TestOffModeNeverTunnels(t *testing.T) {
	var direct, tun atomic.Int32
	tt := newTestTunnel(t, ModeOff, failDialer(&direct), echoDialer("T:", &tun))
	c, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	defer c.Close()
	if code != repHostUnreachable || tun.Load() != 0 {
		t.Fatalf("off mode must not use the tunnel (code %d, tunnel dials %d)", code, tun.Load())
	}
}

func TestRouteSwitchClosesOldConnections(t *testing.T) {
	var direct, tun atomic.Int32
	hold := func(ctx context.Context, network, addr string) (net.Conn, error) {
		direct.Add(1)
		a, b := net.Pipe()
		go func() { io.Copy(io.Discard, b) }() // never answers: a long-lived connection
		return a, nil
	}
	tt := newTestTunnel(t, ModeAuto, hold, echoDialer("T:", &tun))
	c, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	if code != repSucceeded {
		t.Fatalf("connect failed: %d", code)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	gen := tt.RouteGen()
	if err := tt.SetMode(ModeAlways); err != nil {
		t.Fatal(err)
	}
	if tt.RouteGen() != gen+1 {
		t.Fatalf("a route change must bump the generation the shell watches")
	}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("direct connection must be closed when the route switches to the tunnel")
	}
	c.Close()
	c, _ = socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	defer c.Close()
	if got := roundTrip(t, c); got != "T:hi" {
		t.Fatalf("new connection must use the tunnel: got %q", got)
	}
}

func TestEventLogRing(t *testing.T) {
	l := newEventLog(3)
	for i := 0; i < 5; i++ {
		l.add("e%d", i)
	}
	ev := l.last(10)
	if len(ev) != 3 || ev[0].Msg != "e2" || ev[2].Msg != "e4" {
		t.Fatalf("ring order: %+v", ev)
	}
}

func TestAnyCanaryMeansBlocked(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	tt.mu.Lock()
	tt.relays["relay.example.org"] = time.Now()
	tt.mu.Unlock()
	// Relays fail, only the last default canary answers.
	last := DefaultCanaries[len(DefaultCanaries)-1]
	tt.probeTLS = func(_ context.Context, hostport, _ string) error {
		if hostport == last {
			return nil
		}
		return errors.New("refused")
	}
	if res := tt.probe(); res.Relays != 1 || res.RelayOK != 0 || !res.CanaryOK {
		t.Fatalf("one canary answering must count: %+v", res)
	}
	tt.probeTLS = func(context.Context, string, string) error { return errors.New("refused") }
	if res := tt.probe(); res.CanaryOK {
		t.Fatalf("no canary answering must read as offline: %+v", res)
	}
}

func TestEffectiveOrder(t *testing.T) {
	cfg, err := Config{
		Zone: "t.example.org", Key: testKey,
		Resolvers:       []string{"77.88.8.8:53", "10.0.0.1:53"},
		SystemResolvers: []string{"10.0.0.1:53", "fe80::1%wlan0", "garbage", "2001:db8::53"},
		Operator:        "25001",
	}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	eff := cfg.Effective()
	if eff[0] != "10.0.0.1:53" || eff[1] != "[2001:db8::53]:53" {
		t.Fatalf("network resolvers must come first: %v", eff)
	}
	if eff[2] != OperatorResolvers["25001"][0] {
		t.Fatalf("operator resolvers must follow: %v", eff)
	}
	// link extras (77.88.8.8 is also a default: listed once), then defaults
	if eff[2+len(OperatorResolvers["25001"])] != "77.88.8.8:53" {
		t.Fatalf("link resolvers must follow the operator's: %v", eff)
	}
	seen := map[string]bool{}
	for _, r := range eff {
		if seen[r] {
			t.Fatalf("duplicate %s in %v", r, eff)
		}
		seen[r] = true
	}
	if len(eff) != 2+len(OperatorResolvers["25001"])+len(DefaultResolvers) {
		t.Fatalf("network + operator + defaults (link entries overlap them): %d %v", len(eff), eff)
	}
	other, _ := Config{Zone: "t.example.org", Key: testKey, Operator: "99999"}.Normalize()
	if len(other.Effective()) != len(DefaultResolvers) {
		t.Fatalf("unknown operator adds nothing")
	}
}

func TestBytesCountedWhileOpen(t *testing.T) {
	var n atomic.Int32
	// Echo forever: the connection stays open like IMAP IDLE.
	echo := func(ctx context.Context, network, addr string) (net.Conn, error) {
		n.Add(1)
		a, b := net.Pipe()
		go func() { io.Copy(b, b) }()
		return a, nil
	}
	tt := newTestTunnel(t, ModeAlways, echo, echo)
	c, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	if code != repSucceeded {
		t.Fatalf("connect failed: %d", code)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	io.ReadFull(c, make([]byte, 5))
	st := tt.Status(0).Stats["tunnel"]
	if st.Up != 5 || st.Down != 5 || st.Conns != 1 {
		t.Fatalf("live counters on an open connection: %+v", st)
	}
}

func TestProbeUsesTheRelayPort(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	for _, target := range []string{"imap.example.org:993", "smtp.example.org:587"} {
		c, _ := socksConnect(t, tt.Addr(), "u", "p", target)
		c.Close()
	}
	var probed sync.Map
	tt.probeTLS = func(_ context.Context, hostport, _ string) error {
		probed.Store(hostport, true)
		return nil
	}
	tt.probe()
	for _, want := range []string{"imap.example.org:993", "smtp.example.org:443"} {
		if _, ok := probed.Load(want); !ok {
			t.Errorf("expected a TLS probe of %s", want)
		}
	}
}

func TestFixedProbeTargets(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	cfg, err := Config{Zone: "t.example.org", Key: testKey, ProbeTargets: []string{" relay.example.org ", "", "mx.example.org:993"}}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.ProbeTargets, ","); got != "relay.example.org:443,mx.example.org:993" {
		t.Fatalf("probe targets %q", got)
	}
	tt.cfg.ProbeTargets = cfg.ProbeTargets
	tt.useProbeTargets()
	// With fixed probe targets, other hosts opened through the proxy must
	// not become probe targets.
	c, _ := socksConnect(t, tt.Addr(), "u", "p", "allowed.example.net:443")
	c.Close()
	var probed sync.Map
	tt.probeTLS = func(_ context.Context, hostport, _ string) error {
		probed.Store(hostport, true)
		return nil
	}
	if res := tt.probe(); res.Relays != 2 {
		t.Errorf("probed %d hosts, want the 2 fixed ones", res.Relays)
	}
	for _, want := range []string{"relay.example.org:443", "mx.example.org:993"} {
		if _, ok := probed.Load(want); !ok {
			t.Errorf("expected a TLS probe of %s", want)
		}
	}
	if _, ok := probed.Load("allowed.example.net:443"); ok {
		t.Error("a host seen in traffic was probed although targets are fixed")
	}
	nine := make([]string, 9)
	for i := range nine {
		nine[i] = fmt.Sprintf("h%d.example.org", i)
	}
	if _, err := (Config{Zone: "t.example.org", Key: testKey, ProbeTargets: nine}).Normalize(); err == nil {
		t.Error("accepted 9 probe targets")
	}
}

func TestWithoutIPv6(t *testing.T) {
	in := []string{"77.88.8.8:53", "[2a02:6b8::feed:ff]:53", "77.88.8.8:853", "[2a0c:a9c7:8::1]:53"}
	out, n := withoutIPv6(in)
	if n != 2 || strings.Join(out, ",") != "77.88.8.8:53,77.88.8.8:853" {
		t.Fatalf("got %v (%d dropped)", out, n)
	}
}

func TestPrivateResolversOnlyFromTheNetwork(t *testing.T) {
	in := []string{"10.0.0.1:53", "77.88.8.8:53", "10.77.135.4:53", "100.64.1.1:53", "[fd00::1]:53", "192.168.8.1:53"}
	out, n := withoutForeignPrivate(in, []string{"10.0.0.1:53", "192.168.8.1:53"})
	if n != 3 || strings.Join(out, ",") != "10.0.0.1:53,77.88.8.8:53,192.168.8.1:53" {
		t.Fatalf("got %v (%d dropped)", out, n)
	}
}

func TestIPv4First(t *testing.T) {
	in := []string{"[2a00::1]:53", "10.0.0.1:53", "[2a02::2]:53", "77.88.8.8:853"}
	if got := strings.Join(ipv4First(in), ","); got != "10.0.0.1:53,77.88.8.8:853,[2a00::1]:53,[2a02::2]:53" {
		t.Fatalf("got %s", got)
	}
}

func TestFailingTunnelIsLeftAtOnce(t *testing.T) {
	ok := probeResult{Relays: 3, RelayOK: 3, CanaryOK: true}
	if r, _, _, _ := decide(RouteTunnel, ok, 0, 0, false); r != RouteDirect {
		t.Fatal("relays answer directly and the tunnel fails: leave it after one probe")
	}
	if r, _, _, _ := decide(RouteTunnel, ok, 0, 0, true); r != RouteTunnel {
		t.Fatal("a healthy tunnel still waits for recoveredAfter probes")
	}
}

func TestAutoAfterAlwaysStartsDirect(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAlways, echoDialer("D:", &n), echoDialer("T:", &n))
	if err := tt.SetMode(ModeAuto); err != nil {
		t.Fatal(err)
	}
	if s := tt.Status(0); s.Route != "direct" {
		t.Fatalf("auto must start direct, got %s", s.Route)
	}
}

func TestTunnelFailuresMarkItUnhealthy(t *testing.T) {
	var d, tu atomic.Int32
	tt := newTestTunnel(t, ModeAlways, echoDialer("D:", &d), failDialer(&tu))
	c, _ := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	c.Close()
	tt.mu.Lock()
	fails := tt.tunnelFails
	tt.mu.Unlock()
	if fails != 1 {
		t.Fatalf("tunnel failures since the switch: %d", fails)
	}
	tt.SetMode(ModeAuto) // route change resets the count
	tt.mu.Lock()
	fails = tt.tunnelFails
	tt.mu.Unlock()
	if fails != 0 {
		t.Fatalf("count must reset on a route change: %d", fails)
	}
}

func TestRelaysDownThroughTheTunnelKeepTheRoute(t *testing.T) {
	down := probeResult{Relays: 1, CanaryOK: true, TunnelChecked: true}
	r, f, _, s := decide(RouteDirect, down, blockedAfter-1, 0, true)
	if r != RouteDirect || s != stateRelaysDown || f != blockedAfter {
		t.Fatalf("relays failing through a live tunnel too are down, not blocked: %v %d %s", r, f, s)
	}
	up := probeResult{Relays: 1, CanaryOK: true, TunnelChecked: true, TunnelOK: true}
	if r, _, _, _ := decide(RouteDirect, up, blockedAfter-1, 0, true); r != RouteTunnel {
		t.Fatal("a relay answering through the tunnel means blocked: switch")
	}
}

// relayDownTunnel is a test tunnel in auto mode one blocked probe away from
// switching, with relay.example.org failing directly and a canary answering.
func relayDownTunnel(t *testing.T, viaTunnel error, session bool) (*Tunnel, *atomic.Int32) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	tt.mu.Lock()
	tt.relays["relay.example.org"] = time.Now()
	tt.relayPorts["relay.example.org"] = "993"
	tt.failStreak = blockedAfter - 1
	tt.mu.Unlock()
	tt.probeTLS = func(_ context.Context, hostport, _ string) error {
		if hostport == DefaultCanaries[0] {
			return nil
		}
		return errors.New("refused")
	}
	var tunnelProbes atomic.Int32
	tt.probeTunnel = func(_ context.Context, hostport, _ string) error {
		if hostport != "relay.example.org:993" {
			t.Errorf("tunnel probe of %s", hostport)
		}
		tunnelProbes.Add(1)
		return viaTunnel
	}
	tt.established = func() bool { return session }
	return tt, &tunnelProbes
}

func TestSingleRelayDownStaysDirect(t *testing.T) {
	tt, probes := relayDownTunnel(t, errors.New("connection refused"), true)
	tt.applyProbe(tt.confirmViaTunnel(tt.probe()))
	s := tt.Status(10)
	if s.Route != "direct" || s.State != stateRelaysDown || probes.Load() != 1 {
		t.Fatalf("a relay down through a live tunnel must not switch: route %s state %s probes %d", s.Route, s.State, probes.Load())
	}
	if !s.LastResult.TunnelChecked || s.LastResult.TunnelOK {
		t.Fatalf("result must carry the tunnel check: %+v", s.LastResult)
	}
}

func TestRelayReachableThroughTheTunnelSwitches(t *testing.T) {
	tt, _ := relayDownTunnel(t, nil, true)
	tt.applyProbe(tt.confirmViaTunnel(tt.probe()))
	if s := tt.Status(0); s.Route != "tunnel" || s.State != "blocked" {
		t.Fatalf("relay blocked directly but reachable through the tunnel: route %s state %s", s.Route, s.State)
	}
}

func TestNoTunnelSessionSwitchesAnyway(t *testing.T) {
	tt, _ := relayDownTunnel(t, errors.New("timeout"), false)
	tt.applyProbe(tt.confirmViaTunnel(tt.probe()))
	if s := tt.Status(0); s.Route != "tunnel" || s.LastResult.TunnelChecked {
		t.Fatalf("a failure without a live session proves nothing: route %s %+v", s.Route, s.LastResult)
	}
}

func TestTunnelCheckedOnlyBeforeASwitch(t *testing.T) {
	tt, probes := relayDownTunnel(t, nil, true)
	tt.mu.Lock()
	tt.failStreak = 0
	tt.mu.Unlock()
	tt.applyProbe(tt.confirmViaTunnel(tt.probe()))
	if probes.Load() != 0 {
		t.Fatalf("the first blocked probe must not touch the tunnel (%d probes)", probes.Load())
	}
}

// probeLog records every TLS probe and answers ok for the hosts in ok.
func probeLog(ok ...string) (func(context.Context, string, string) error, func() []string) {
	var mu sync.Mutex
	var seen []string
	return func(_ context.Context, hostport, _ string) error {
			mu.Lock()
			seen = append(seen, hostport)
			mu.Unlock()
			if slices.Contains(ok, hostport) {
				return nil
			}
			return errors.New("refused")
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), seen...)
		}
}

func TestCanariesSkippedWhenARelayAnswers(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	tt.mu.Lock()
	tt.relays["relay.example.org"] = time.Now()
	tt.mu.Unlock()
	probe, seen := probeLog("relay.example.org:443")
	tt.probeTLS = probe
	res := tt.probe()
	if res.RelayOK != 1 || res.CanaryChecked || res.CanaryOK {
		t.Fatalf("relay answered, canaries must be left alone: %+v", res)
	}
	if got := seen(); !slices.Equal(got, []string{"relay.example.org:443"}) {
		t.Fatalf("probed %v", got)
	}
}

func TestCanariesSkippedWithoutRelays(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	probe, seen := probeLog(DefaultCanaries...)
	tt.probeTLS = probe
	if res := tt.probe(); res.CanaryChecked || len(seen()) != 0 {
		t.Fatalf("nothing to compare the canaries with: %+v, probed %v", res, seen())
	}
}

func TestCanariesOneAtATimeLastGoodFirst(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	tt.mu.Lock()
	tt.relays["relay.example.org"] = time.Now()
	tt.mu.Unlock()

	// Every canary answers: the first one is enough.
	probe, seen := probeLog(DefaultCanaries...)
	tt.probeTLS = probe
	if res := tt.probe(); !res.CanaryChecked || !res.CanaryOK {
		t.Fatalf("canary must answer: %+v", res)
	}
	if got := seen(); !slices.Equal(got, []string{"relay.example.org:443", DefaultCanaries[0]}) {
		t.Fatalf("want one canary handshake, probed %v", got)
	}

	// The first fails at once, the second answers and becomes the first next time.
	probe, seen = probeLog(DefaultCanaries[1])
	tt.probeTLS = probe
	if res := tt.probe(); !res.CanaryOK {
		t.Fatalf("second canary must count: %+v", res)
	}
	if got := seen(); !slices.Equal(got, []string{"relay.example.org:443", DefaultCanaries[0], DefaultCanaries[1]}) {
		t.Fatalf("probed %v", got)
	}
	probe, seen = probeLog(DefaultCanaries[1])
	tt.probeTLS = probe
	tt.probe()
	if got := seen(); !slices.Equal(got, []string{"relay.example.org:443", DefaultCanaries[1]}) {
		t.Fatalf("last good canary must go first, probed %v", got)
	}
}

func TestSilentCanaryDoesNotHoldUpTheNext(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	tt.mu.Lock()
	tt.relays["relay.example.org"] = time.Now()
	tt.mu.Unlock()
	// The first canary hangs until cancelled (a dropped SYN), the second answers.
	var cancelled atomic.Bool
	tt.probeTLS = func(ctx context.Context, hostport, _ string) error {
		switch hostport {
		case DefaultCanaries[0]:
			<-ctx.Done()
			cancelled.Store(true)
			return ctx.Err()
		case DefaultCanaries[1]:
			return nil
		}
		return errors.New("refused")
	}
	start := time.Now()
	if res := tt.probe(); !res.CanaryOK {
		t.Fatalf("second canary must count: %+v", res)
	}
	if d := time.Since(start); d >= probeTimeout {
		t.Fatalf("waited %v for the silent canary", d)
	}
	deadline := time.Now().Add(time.Second)
	for !cancelled.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cancelled.Load() {
		t.Fatal("the silent canary's probe must be cancelled once another answers")
	}
}

func TestDialFallsThroughRoutesInOrder(t *testing.T) {
	var tried []string
	fail := func(name string) dialFunc {
		return func(context.Context, string, string) (net.Conn, error) {
			tried = append(tried, name)
			return nil, errors.New(name + " down")
		}
	}
	tt := newTestTunnel(t, ModeAuto, fail("direct"), fail("tunnel"))
	defer tt.Close()

	_, r, err := tt.dial("relay.example.org:993")
	if err == nil || r != RouteTunnel || strings.Join(tried, ",") != "direct,tunnel" {
		t.Fatalf("auto: route %v err %v tried %v", r, err, tried)
	}
	if got := tt.Status(0).Stats["direct"].Fails; got != 1 {
		t.Errorf("the direct failure before falling through counts once, got %d", got)
	}

	tried = nil
	tt.SetMode(ModeOff)
	if _, r, _ := tt.dial("relay.example.org:993"); r != RouteDirect || strings.Join(tried, ",") != "direct" {
		t.Fatalf("off: never falls through to the tunnel, route %v tried %v", r, tried)
	}
}

func TestRouteWithoutDialerIsSkipped(t *testing.T) {
	down := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("down") }
	tt := newTestTunnel(t, ModeAuto, down, nil)
	defer tt.Close()
	if _, r, err := tt.dial("relay.example.org:993"); err == nil || r != RouteDirect {
		t.Fatalf("with no tunnel set up the direct error comes back: route %v err %v", r, err)
	}
	s := tt.Status(0)
	if strings.Join(s.Routes, ",") != "direct" {
		t.Errorf("routes %v, want only direct", s.Routes)
	}
	if _, ok := s.Stats["tunnel"]; !ok {
		t.Errorf("stats keep every route's key for the app: %v", s.Stats)
	}
}

func TestDecideUsesTheSmartRoute(t *testing.T) {
	smart := probeResult{Relays: 1, CanaryOK: true, CanaryChecked: true, SmartChecked: true, SmartOK: true}
	noSmart := probeResult{Relays: 1, CanaryOK: true, CanaryChecked: true, SmartChecked: true}

	if r, _, _, _ := decide(RouteDirect, smart, 0, 0, true); r != RouteDirect {
		t.Fatalf("one blocked probe must not switch yet, got %v", r)
	}
	if r, _, _, s := decide(RouteDirect, smart, blockedAfter-1, 0, true); r != RouteSmart || s != "blocked" {
		t.Fatalf("blocked with a working strategy goes smart, got %v %s", r, s)
	}
	if r, _, _, _ := decide(RouteTunnel, smart, 5, 0, true); r != RouteSmart {
		t.Fatalf("from the tunnel a working strategy is preferred, got %v", r)
	}
	if r, _, _, _ := decide(RouteSmart, noSmart, 5, 0, true); r != RouteTunnel {
		t.Fatalf("a strategy that stopped working falls back to the tunnel, got %v", r)
	}
	ok := probeResult{Relays: 1, RelayOK: 1}
	r := RouteSmart
	for i := 0; i < recoveredAfter; i++ {
		r, _, _, _ = decide(r, ok, 0, i, true)
	}
	if r != RouteDirect {
		t.Fatalf("direct answering again for recoveredAfter probes leaves smart, got %v", r)
	}
}

func TestSmartCheckFindsAndKeepsAStrategy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	probeRoots = pool
	defer func() { probeRoots = nil }()

	down := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("blocked") }
	tt := newTestTunnel(t, ModeAuto, down, down)
	defer tt.Close()
	var d net.Dialer
	tt.strategies = []strategy{{"broken", down}, {"works", d.DialContext}}

	// httptest's certificate is for example.com and 127.0.0.1.
	host := net.JoinHostPort("example.com", srv.Listener.Addr().(*net.TCPAddr).AddrPort().String()[len("127.0.0.1:"):])
	tt.strategies[1].dial = func(ctx context.Context, n, _ string) (net.Conn, error) {
		return d.DialContext(ctx, n, srv.Listener.Addr().String())
	}
	if !tt.smartCheck([]string{host}) {
		t.Fatal("no strategy found")
	}
	if s := tt.Status(0); s.Strategy != "works" || strings.Join(s.Routes, ",") != "direct,smart,tunnel" {
		t.Fatalf("strategy %q routes %v", s.Strategy, s.Routes)
	}
	tt.strategies = []strategy{{"broken", down}}
	if !tt.smartCheck([]string{host}) {
		t.Fatal("the working strategy must be kept while it still works")
	}
	tt.mu.Lock()
	tt.strategy.dial = down
	tt.mu.Unlock()
	if tt.smartCheck([]string{host}) {
		t.Fatal("with nothing working the smart route must go away")
	}
	if s := tt.Status(0); s.Strategy != "" || strings.Join(s.Routes, ",") != "direct,tunnel" {
		t.Fatalf("after failure: strategy %q routes %v", s.Strategy, s.Routes)
	}
}

func TestSilentConnectionTriggersAProbe(t *testing.T) {
	saved := firstResponseTimeout
	firstResponseTimeout = 100 * time.Millisecond
	defer func() { firstResponseTimeout = saved }()

	silent := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go io.Copy(io.Discard, b) // reads everything, answers nothing
		return a, nil
	}
	tt := newTestTunnel(t, ModeAuto, silent, nil)
	defer tt.Close()
	c, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	if code != repSucceeded {
		t.Fatalf("socks reply %d", code)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	select {
	case <-tt.kick:
	case <-time.After(2 * time.Second):
		t.Fatal("a connection with no answer must kick a probe")
	}
}

func TestKnownStrategySwitchesOnFirstBlockedProbe(t *testing.T) {
	known := probeResult{Relays: 1, CanaryOK: true, SmartChecked: true, SmartOK: true, SmartKnown: true}
	if r, _, _, _ := decide(RouteDirect, known, 0, 0, true); r != RouteSmart {
		t.Fatalf("a strategy known on this network switches at once, got %v", r)
	}
}

func TestStrategyIsRememberedPerNetwork(t *testing.T) {
	down := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("down") }
	var d net.Dialer
	mk := func(resolvers ...string) *Tunnel {
		tt := newTestTunnel(t, ModeAuto, down, down)
		tt.cfg.SystemResolvers = resolvers
		tt.strategies = []strategy{{"a", down}, {"b", d.DialContext}}
		return tt
	}
	knownStrategies.Lock()
	knownStrategies.m = map[string]string{}
	knownStrategies.Unlock()

	first := mk("192.0.2.53:53")
	first.mu.Lock()
	first.strategy, first.ways[RouteSmart] = &first.strategies[1], way{d.DialContext, time.Second}
	first.mu.Unlock()
	knownStrategies.Lock()
	knownStrategies.m[networkKey(first.cfg)] = "b"
	knownStrategies.Unlock()
	first.Close()

	again := mk("192.0.2.53:53")
	defer again.Close()
	again.useKnownStrategy()
	if s := again.Status(0); s.Strategy != "b" {
		t.Fatalf("same network: strategy %q, want b", s.Strategy)
	}
	other := mk("198.51.100.53:53")
	defer other.Close()
	other.useKnownStrategy()
	if s := other.Status(0); s.Strategy != "" {
		t.Fatalf("another network starts without one, got %q", s.Strategy)
	}
}

func TestParseWebLink(t *testing.T) {
	// A key with '+' and '/' in standard base64, unescaped as people paste it.
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb, 0xef}, 16))
	if !strings.ContainsAny(key, "+/") {
		t.Fatal("test key must contain + or /")
	}
	want := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb, 0xef}, 16))
	for _, link := range []string{
		"https://passage.gluek.info/link#v=1&zone=t.example.org&key=" + key + "&r=192.0.2.1,192.0.2.2",
		"https://passage.gluek.info/link/#v=1&zone=t.example.org&key=" + key + "&r=192.0.2.1,192.0.2.2",
		"passage://tunnel?v=1&zone=t.example.org&key=" + key + "&r=192.0.2.1,192.0.2.2",
	} {
		cfg, err := ParseLink(link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		if cfg.Zone != "t.example.org" || cfg.Key != want || strings.Join(cfg.Resolvers, ",") != "192.0.2.1:53,192.0.2.2:53" {
			t.Errorf("%s: %+v", link, cfg)
		}
	}
	for _, bad := range []string{
		"https://example.org/link#v=1&zone=t.example.org&key=" + key,
		"https://passage.gluek.info/#v=1&zone=t.example.org&key=" + key,
		"https://passage.gluek.info/other#v=1&zone=t.example.org&key=" + key,
		"http://passage.gluek.info/link#v=1&zone=t.example.org&key=" + key,
	} {
		if _, err := ParseLink(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
