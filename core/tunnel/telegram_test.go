package tunnel

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelegramClassification(t *testing.T) {
	for addr, want := range map[string]groupKind{
		"149.154.167.41:443":           kindTelegram,
		"91.108.56.191:5222":           kindTelegram,
		"185.76.151.7:80":              kindTelegram,
		"[2001:67c:4e8:f002::a]:443":   kindTelegram,
		"[2a0a:f280:1::1]:443":         kindTelegram,
		"[::ffff:149.154.175.100]:443": kindTelegram,
		"192.0.2.1:443":                kindOther,
		"149.154.176.1:443":            kindOther, // just past 149.154.160.0/20
		"[2001:db8::1]:993":            kindOther,
		"[2001:67c:4e9::1]:443":        kindOther,
		"relay.example.org:993":        kindRelay,
	} {
		if got := kindOf(addr); got != want {
			t.Errorf("%s: %s, want %s", addr, got, want)
		}
	}
	for _, dc := range telegramDCs {
		if kindOf(dc) != kindTelegram {
			t.Errorf("DC %s outside the Telegram ranges", dc)
		}
	}
}

// fakeDC serves one MTProto exchange per connection: it checks the
// req_pq_multi and answers resPQ with the nonce from answer.
func fakeDC(t *testing.T, answer func(nonce []byte) []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				req := make([]byte, 2+40)
				if _, err := io.ReadFull(c, req); err != nil {
					return
				}
				le := binary.LittleEndian
				if req[0] != 0xef || req[1] != 10 || le.Uint64(req[2:]) != 0 || le.Uint64(req[10:])%4 != 0 ||
					le.Uint64(req[10:])>>32 == 0 || le.Uint32(req[18:]) != 20 || le.Uint32(req[22:]) != 0xbe7e8ef1 {
					t.Errorf("bad req_pq_multi % x", req)
					return
				}
				body := answer(req[26:42])
				if body == nil {
					io.Copy(io.Discard, c) // silent
					return
				}
				msg := le.AppendUint64(nil, 0)
				msg = le.AppendUint64(msg, uint64(time.Now().Unix())<<32|1)
				msg = le.AppendUint32(msg, uint32(len(body)))
				msg = append(msg, body...)
				c.Write(append([]byte{byte(len(msg) / 4)}, msg...))
			}()
		}
	}()
	return ln.Addr().String()
}

func resPQ(nonce []byte) []byte {
	le := binary.LittleEndian
	b := le.AppendUint32(nil, 0x05162463)
	b = append(b, nonce...)
	b = append(b, make([]byte, 16)...)                                        // server_nonce
	b = append(b, 8, 0x17, 0xed, 0x48, 0x94, 0x1a, 0x08, 0xf9, 0x81, 0, 0, 0) // pq:bytes
	b = le.AppendUint32(b, 0x1cb5c415)                                        // vector
	b = le.AppendUint32(b, 1)
	return le.AppendUint64(b, 0xc3b42b026ce86b21)
}

func TestMTProtoProbe(t *testing.T) {
	d := &net.Dialer{}
	probe := func(addr string, timeout time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return probeMTProtoVia(ctx, d.DialContext, addr)
	}
	if err := probe(fakeDC(t, resPQ), 2*time.Second); err != nil {
		t.Fatalf("resPQ with our nonce: %v", err)
	}
	wrong := fakeDC(t, func(nonce []byte) []byte {
		n := slices.Clone(nonce)
		n[0] ^= 1
		return resPQ(n)
	})
	if err := probe(wrong, 2*time.Second); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("wrong nonce must fail: %v", err)
	}
	notResPQ := fakeDC(t, func(nonce []byte) []byte {
		b := resPQ(nonce)
		b[0] ^= 1
		return b
	})
	if err := probe(notResPQ, 2*time.Second); err == nil {
		t.Fatal("another constructor must fail")
	}
	start := time.Now()
	err := probe(fakeDC(t, func([]byte) []byte { return nil }), 200*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) && !isTimeout(err) || time.Since(start) > 2*time.Second {
		t.Fatalf("silent DC must time out: %v after %v", err, time.Since(start))
	}
}

// groupsTunnel is a test tunnel with a mail relay and Telegram traffic seen,
// the relay answering directly as long as mailOK is set, the DCs never.
func groupsTunnel(t *testing.T, direct dialFunc) (*Tunnel, *atomic.Bool) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, direct, echoDialer("T:", &n))
	tt.testGroup("relay.example.org:993")
	tt.testGroup("149.154.167.50:443")
	var mailOK atomic.Bool
	mailOK.Store(true)
	tt.probeTLS = func(_ context.Context, hostport, _ string) error {
		if hostport == DefaultCanaries[0] || hostport == "relay.example.org:993" && mailOK.Load() {
			return nil
		}
		return errors.New("refused")
	}
	tt.probeMT = func(context.Context, string) error { return errors.New("refused") }
	return tt, &mailOK
}

func TestTelegramBlockedKeepsMailDirect(t *testing.T) {
	var n atomic.Int32
	tt, _ := groupsTunnel(t, echoDialer("D:", &n))
	for range blockedAfter {
		tt.check()
	}
	s := tt.Status(0)
	mail, tg := s.Groups["relay.example.org"], s.Groups["telegram"]
	if s.Route != "direct" || mail.Route != "direct" || mail.State != "direct" {
		t.Fatalf("mail must stay direct: %s / %+v", s.Route, mail)
	}
	if tg.Route != "tunnel" || tg.State != "blocked" || !tg.LastResult.CanaryOK {
		t.Fatalf("telegram must move to the tunnel: %+v", tg)
	}
	if !slices.Contains(tg.Targets, "149.154.167.50:443") || !slices.Contains(tg.Targets, telegramDCs[0]) {
		t.Fatalf("telegram probe targets: %v", tg.Targets)
	}
}

func TestMailBlockedKeepsTelegramDirect(t *testing.T) {
	var n atomic.Int32
	tt, mailOK := groupsTunnel(t, echoDialer("D:", &n))
	mailOK.Store(false)
	tt.probeMT = func(context.Context, string) error { return nil }
	for range blockedAfter {
		tt.check()
	}
	s := tt.Status(0)
	if s.Route != "tunnel" || s.Groups["telegram"].Route != "direct" {
		t.Fatalf("mail blocked must not move telegram: mail %s telegram %+v", s.Route, s.Groups["telegram"])
	}
}

func TestTelegramNeverUsesSmart(t *testing.T) {
	var n atomic.Int32
	tt, _ := groupsTunnel(t, echoDialer("D:", &n))
	tt.mu.Lock()
	tt.ways[RouteSmart] = way{echoDialer("S:", &n), time.Second}
	if r := tt.nextRouteLocked(tt.telegram, RouteDirect); r != RouteTunnel {
		t.Errorf("telegram falls through to %s", r)
	}
	if r := tt.nextRouteLocked(tt.relays["relay.example.org"], RouteDirect); r != RouteSmart {
		t.Errorf("mail falls through to %s", r)
	}
	tt.mu.Unlock()
	works := probeResult{Relays: 5, CanaryOK: true, CanaryChecked: true, SmartChecked: true, SmartOK: true, SmartKnown: true}
	for range blockedAfter {
		tt.applyProbe(tt.relays["relay.example.org"], works)
		tt.applyProbe(tt.telegram, works)
	}
	s := tt.Status(0)
	if s.Route != "smart" || s.Groups["telegram"].Route != "tunnel" {
		t.Fatalf("mail %s, telegram %s: telegram must not take the smart route", s.Route, s.Groups["telegram"].Route)
	}
}

func TestGroupSwitchClosesOnlyItsConnections(t *testing.T) {
	hold := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go io.Copy(io.Discard, b)
		return a, nil
	}
	tt, _ := groupsTunnel(t, hold)
	mail, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
	tg, code2 := socksConnect(t, tt.Addr(), "u", "p", "149.154.167.50:443")
	if code != repSucceeded || code2 != repSucceeded {
		t.Fatalf("connect failed: %d %d", code, code2)
	}
	defer mail.Close()
	defer tg.Close()
	for range blockedAfter {
		tt.check()
	}
	tg.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := tg.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("the telegram connection must be closed on its switch: %v", err)
	}
	mail.SetDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := mail.Read(make([]byte, 1)); !isTimeout(err) {
		t.Fatalf("the mail connection must stay open: %v", err)
	}
	tt.mu.Lock()
	routes := map[*group]Route{}
	for c := range tt.conns {
		routes[c.group] = c.route
	}
	tt.mu.Unlock()
	if len(routes) != 1 || routes[tt.relays["relay.example.org"]] != RouteDirect {
		t.Fatalf("open connections: %v", routes)
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func TestTelegramRelaysDownThroughTheTunnel(t *testing.T) {
	var n atomic.Int32
	tt, _ := groupsTunnel(t, echoDialer("D:", &n))
	var viaTunnel atomic.Int32
	tt.probeMTTunnel = func(context.Context, string) error {
		viaTunnel.Add(1)
		return errors.New("refused")
	}
	tt.probeTunnel = func(context.Context, string, string) error { return nil }
	tt.established = func() bool { return true }
	for range blockedAfter {
		tt.check()
	}
	if g := tt.Status(0).Groups["telegram"]; g.Route != "direct" || g.State != stateRelaysDown || viaTunnel.Load() == 0 {
		t.Fatalf("DCs failing through a live tunnel too must keep the route: %+v (%d tunnel probes)", g, viaTunnel.Load())
	}
}

func TestTelegramTargets(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), nil)
	tt.mu.Lock()
	if got := tt.targetsLocked(tt.telegram); got != nil {
		t.Errorf("unused group has targets %v", got)
	}
	tt.mu.Unlock()
	if res := tt.probe(); res[tt.telegram].Relays != 0 {
		t.Errorf("unused group was probed: %+v", res[tt.telegram])
	}
	for i := range maxTelegramHosts + 4 {
		tt.learn(tt.telegram, net.JoinHostPort(net.IPv4(149, 154, 164, byte(i)).String(), "443"))
	}
	tt.learn(tt.telegram, telegramDCs[1])
	tt.mu.Lock()
	got := tt.targetsLocked(tt.telegram)
	kept := len(tt.telegram.hosts)
	tt.mu.Unlock()
	if len(got) != maxProbedRelays+len(telegramDCs) || got[0] != "149.154.164.35:443" || kept != maxTelegramHosts {
		t.Fatalf("targets %v, %d hosts kept", got, kept)
	}
	if !slices.Equal(got[len(got)-len(telegramDCs):], telegramDCs) {
		t.Fatalf("DCs must be probed: %v", got)
	}
}

func TestStatusHasGroups(t *testing.T) {
	var n atomic.Int32
	tt, _ := groupsTunnel(t, echoDialer("D:", &n))
	for range blockedAfter {
		tt.check()
	}
	b, err := json.Marshal(tt.Status(0))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Route  string `json:"route"`
		Groups map[string]struct {
			Route      string         `json:"route"`
			State      string         `json:"state"`
			LastResult map[string]any `json:"last_result"`
			Targets    []string       `json:"targets"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.Route != "direct" || s.Groups["relay.example.org"].Route != "direct" || s.Groups["telegram"].Route != "tunnel" ||
		s.Groups["telegram"].LastResult["relays"] == nil || len(s.Groups["relay.example.org"].Targets) != 1 {
		t.Fatalf("status JSON: %s", b)
	}
}
