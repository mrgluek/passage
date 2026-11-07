package tunnel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

const turnTestSecret = "test-secret"

type testTURN struct {
	t       *testing.T
	exit    net.Addr
	loss    bool
	static  bool
	padded  atomic.Int64
	inits   atomic.Int64
	packets atomic.Int64
}
type testTURNPeer struct {
	server *testTURN
	relay  net.PacketConn
	write  func([]byte) error
	key    []byte
	bound  bool
	stale  bool
}

func (s *testTURN) peer(write func([]byte) error) *testTURNPeer {
	relay, err := net.ListenPacket("udp4", "localhost:0")
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { relay.Close() })
	p := &testTURNPeer{server: s, relay: relay, write: write}
	go func() {
		for {
			b := make([]byte, 1200)
			n, _, err := relay.ReadFrom(b)
			if err != nil {
				return
			}
			b = b[:n]
			wire := []byte{0x40, 0}
			wire = binary.BigEndian.AppendUint16(wire, uint16(n))
			wire = append(wire, b...)
			for len(wire)%4 != 0 {
				wire = append(wire, 0)
			}
			s.forward(func() { write(wire) })
		}
	}()
	return p
}
func (s *testTURN) forward(send func()) {
	n := s.packets.Add(1)
	if s.loss && (n <= 3 || n%7 == 0) {
		return
	}
	if s.loss && n%5 == 0 {
		time.AfterFunc(25*time.Millisecond, send)
	} else {
		send()
	}
	if s.loss && n%11 == 0 {
		send()
	}
}
func (p *testTURNPeer) handle(b []byte) {
	if len(b) >= 4 && binary.BigEndian.Uint16(b) == 0x4000 {
		n := int(binary.BigEndian.Uint16(b[2:]))
		if len(b) != (n+7)&^3 || n > 1200 || n > len(b)-4 {
			p.server.t.Error("ChannelData missing padding or exceeds MTU")
			return
		}
		p.server.padded.Add(1)
		if !p.bound {
			return
		}
		data := bytes.Clone(b[4 : 4+n])
		if len(data) > 0 && data[0] == 1 {
			p.server.inits.Add(1)
		}
		p.server.forward(func() { p.relay.WriteTo(data, p.server.exit) })
		return
	}
	a, err := turnAttrs(b)
	if err != nil {
		return
	}
	kind := binary.BigEndian.Uint16(b)
	id := b[8:20]
	challenge := func(code byte, nonce string) {
		p.write(turnMessage(kind|0x110, id, []turnAttr{{9, []byte{0, 0, 4, code}}, {0x14, []byte("example.org")}, {0x15, []byte(nonce)}}, nil))
	}
	if len(a[6]) == 0 {
		challenge(1, "nonce")
		return
	}
	username := string(a[6])
	password := "password"
	if p.server.static {
		if username != "user" {
			p.server.t.Error("wrong static user")
			return
		}
	} else {
		expiry, err := strconv.ParseInt(username, 10, 64)
		if err != nil || time.Until(time.Unix(expiry, 0)) < 50*time.Minute {
			p.server.t.Error("invalid timed username")
			return
		}
		m := hmac.New(sha1.New, []byte(turnTestSecret))
		m.Write(a[6])
		password = base64.StdEncoding.EncodeToString(m.Sum(nil))
	}
	key := md5.Sum([]byte(username + ":example.org:" + password))
	p.key = key[:]
	if !turnIntegrity(b, p.key) {
		p.server.t.Error("invalid request integrity")
		return
	}
	if kind == 9 && !p.stale {
		p.stale = true
		challenge(38, "fresh-nonce")
		return
	}
	if kind == 9 {
		if string(a[0x15]) != "fresh-nonce" || !bytes.Equal(a[0xc], turnU32(0x40000000)) {
			p.server.t.Error("invalid channel bind")
			return
		}
		peer := a[0x12]
		address, ok := p.server.exit.(*net.UDPAddr)
		if !ok || len(peer) != 8 || peer[1] != 1 || int(binary.BigEndian.Uint16(peer[2:])^uint16(turnCookie>>16)) != address.Port {
			p.server.t.Error("invalid XOR peer address")
			return
		}
		mask := turnU32(turnCookie)
		ip := make(net.IP, 4)
		for i := range ip {
			ip[i] = peer[4+i] ^ mask[i]
		}
		if !ip.Equal(address.IP) {
			p.server.t.Error("wrong XOR peer IP")
			return
		}
		p.bound = true
	}
	if kind == 3 && !bytes.Equal(a[0x19], turnU32(17<<24)) {
		p.server.t.Error("invalid requested transport")
		return
	}
	p.write(turnMessage(kind|0x100, id, []turnAttr{{0xd, turnU32(600)}}, p.key))
}
func testReadTURN(c net.Conn) ([]byte, error) {
	h := make([]byte, 4)
	if _, err := io.ReadFull(c, h); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(h[2:]))
	if h[0]&0xc0 == 0 {
		n += 16
	}
	body := make([]byte, (n+3)&^3)
	_, err := io.ReadFull(c, body)
	return append(h, body...), err
}
func turnFixture(t *testing.T, mode string, loss bool) (*WSSClient, *TURNExit, *testTURN) {
	t.Helper()
	target, err := net.Listen("tcp4", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close() })
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	exit, err := NewTURNExit(wssTestKey, func(host string, port uint16) (net.Conn, byte) {
		if host != "mail.example.org" || port != 443 {
			return nil, 1
		}
		c, err := net.Dial("tcp", target.Addr().String())
		if err != nil {
			return nil, 2
		}
		return c, 0
	})
	if err != nil {
		t.Fatal(err)
	}
	socket, err := net.ListenPacket("udp4", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	go exit.Serve(socket)
	t.Cleanup(func() { exit.Close(); socket.Close() })
	srv := &testTURN{t: t, exit: socket.LocalAddr(), loss: loss, static: mode == "tcp"}
	p := links.TURNParams{Secret: turnTestSecret, Key: wssTestKey, Exit: socket.LocalAddr().String()}
	var address net.Addr
	if mode == "udp" {
		relay, err := net.ListenPacket("udp4", "localhost:0")
		if err != nil {
			t.Fatal(err)
		}
		address = relay.LocalAddr()
		t.Cleanup(func() { relay.Close() })
		go func() {
			peers := map[string]*testTURNPeer{}
			for {
				b := make([]byte, 65536)
				n, addr, err := relay.ReadFrom(b)
				if err != nil {
					return
				}
				peer := peers[addr.String()]
				if peer == nil {
					peer = srv.peer(func(b []byte) error { _, err := relay.WriteTo(b, addr); return err })
					peers[addr.String()] = peer
				}
				peer.handle(b[:n])
			}
		}()
	} else {
		ln, err := net.Listen("tcp4", "localhost:0")
		if err != nil {
			t.Fatal(err)
		}
		address = ln.Addr()
		if mode == "tls" {
			cert := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			cfg := cert.TLS.Clone()
			pool := x509.NewCertPool()
			pool.AddCert(cert.Certificate())
			probeRoots = pool
			t.Cleanup(func() { probeRoots = nil })
			cert.Close()
			ln = tls.NewListener(ln, cfg)
			p.SNI = "example.com"
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				t.Cleanup(func() { c.Close() })
				go func() {
					defer c.Close()
					var mu sync.Mutex
					peer := srv.peer(func(b []byte) error { mu.Lock(); defer mu.Unlock(); _, err := c.Write(b); return err })
					for {
						b, err := testReadTURN(c)
						if err != nil {
							return
						}
						peer.handle(b)
					}
				}()
			}
		}()
	}
	host, port, _ := net.SplitHostPort(address.String())
	p.DialIP = host
	p.Relay = "turn:turn.example.com:" + port
	if mode == "tls" {
		p.Relay = "turns:turn.example.com:" + port
	}
	if mode == "tcp" {
		p.Relay += "?transport=tcp"
		p.Secret = ""
		p.Username = "user"
		p.Credential = "password"
	}
	client, err := NewTURNClient(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client, exit, srv
}

func TestTURNEndToEnd(t *testing.T) {
	for _, mode := range []string{"udp", "tcp", "tls"} {
		t.Run(mode, func(t *testing.T) {
			client, exit, srv := turnFixture(t, mode, false)
			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				conn, err := client.DialContext(ctx, "tcp", "mail.example.org:443")
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(15 * time.Second))
					if err := wssRoundTrip(conn, bytes.Repeat([]byte("turn"), 32768)); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if srv.padded.Load() == 0 {
				t.Fatal("no padded ChannelData")
			}
			if client.received() == 0 {
				t.Fatal("TURN network receive count missing")
			}
			exit.mu.Lock()
			n := len(exit.sessions)
			exit.mu.Unlock()
			if n != 1 {
				t.Fatalf("got %d sessions", n)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := client.DialContext(ctx, "tcp", "other.example.org:443"); !errors.Is(err, errDestinationRefused) {
				t.Fatalf("policy refusal: %v", err)
			}
		})
	}
}
func TestTURNLossReorderingAndDuplication(t *testing.T) {
	client, _, srv := turnFixture(t, "udp", true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, "tcp", "mail.example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(45 * time.Second))
	if err := wssRoundTrip(c, bytes.Repeat([]byte("loss"), 32768)); err != nil {
		t.Fatal(err)
	}
	if srv.inits.Load() < 2 {
		t.Fatal("Init retransmission not exercised")
	}
}
func TestTURNExitAuthenticationSilence(t *testing.T) {
	client, exit, _ := turnFixture(t, "udp", false)
	client.params.Key = bytes.Repeat([]byte{9}, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	if _, err := client.DialContext(ctx, "tcp", "mail.example.org:443"); err == nil {
		t.Fatal("wrong key accepted")
	}
	exit.mu.Lock()
	address := exit.conn.LocalAddr().String()
	exit.mu.Unlock()
	c, err := net.Dial("udp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(150 * time.Millisecond))
	c.Write(bytes.Repeat([]byte{1}, 100))
	b := make([]byte, 1200)
	if n, err := c.Read(b); n != 0 || err == nil {
		t.Fatal("unauthenticated packet received response")
	}
	exit.mu.Lock()
	defer exit.mu.Unlock()
	if len(exit.sessions) != 0 {
		t.Fatal("unauthenticated session created")
	}
}
func TestTURNRoutePoolAndCheck(t *testing.T) {
	client, _, _ := turnFixture(t, "udp", false)
	p := client.turnParams
	r := links.Route{ID: "relay", Type: "turn", Wire: "turn-v1", Params: map[string]any{"relay": p.Relay, "exit": p.Exit, "key": p.Key, "secret": turnTestSecret, "dial_ip": p.DialIP}}
	cfg, err := (Config{Mode: "turn", Routes: []links.Route{r}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := newRoutePool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rp := pool.(*routePool)
	if rp.kinds[0] != "turn" || rp.servers[0].addr != "" || rp.servers[0].fresh == nil || rp.servers[0].received == nil {
		t.Fatal("TURN route metadata missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := checkRoute(ctx, "turn", rp.servers[0], nil, nil)
	if got := stageNames(result.Stages); got != "app" {
		t.Fatalf("UDP stages %s", got)
	}
	p.Relay = "turns:turn.example.com:443"
	p.SNI = "example.org"
	s := turnServer(p, client)
	if !s.tls || s.name != "example.org" || s.addr != net.JoinHostPort(p.DialIP, "443") {
		t.Fatalf("TLS server: %+v", s)
	}
}
func TestTURNSTUNIntegrityAndIPv6(t *testing.T) {
	id := []byte("abcdefghijkl")
	key := []byte("integrity")
	b := turnMessage(9, id, []turnAttr{{0x12, turnPeer("[2001:db8::1]:34780")}}, key)
	if !turnIntegrity(b, key) {
		t.Fatal("integrity failed")
	}
	a, err := turnAttrs(b)
	if err != nil {
		t.Fatal(err)
	}
	peer := a[0x12]
	mask := binary.BigEndian.AppendUint32(nil, turnCookie)
	mask = append(mask, id...)
	ip := make(net.IP, 16)
	for i := range ip {
		ip[i] = peer[4+i] ^ mask[i]
	}
	if ip.String() != "2001:db8::1" {
		t.Fatal(ip)
	}
	b[len(b)-1] ^= 1
	if turnIntegrity(b, key) {
		t.Fatal("tamper accepted")
	}
	for n := 0; n < 20; n++ {
		if _, err := turnAttrs(make([]byte, n)); err == nil {
			t.Fatal(fmt.Sprintf("accepted %d bytes", n))
		}
	}
}

func TestTURNInitReplayAndAuthenticatedMigration(t *testing.T) {
	exit, err := NewTURNExit(wssTestKey, func(string, uint16) (net.Conn, byte) { return nil, 1 })
	if err != nil {
		t.Fatal(err)
	}
	defer exit.Close()
	socket, err := net.ListenPacket("udp4", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	exit.conn = socket
	peers := make([]net.PacketConn, 2)
	for i := range peers {
		peers[i], err = net.ListenPacket("udp4", "localhost:0")
		if err != nil {
			t.Fatal(err)
		}
		defer peers[i].Close()
	}
	token, nonce := wssToken(exit.key, time.Now())
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	sign := func(kind byte, seq uint32) []byte {
		b := []byte{kind}
		if kind == 1 {
			b = append(b, raw...)
		} else {
			b = append(b, nonce...)
		}
		b = binary.BigEndian.AppendUint32(b, seq)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, 0)
		m := hmac.New(sha256.New, wssKey(exit.key, nonce, "pkt-c2s"))
		m.Write(b)
		return append(b, m.Sum(nil)[:16]...)
	}
	init := sign(1, 0)
	exit.packet(init, peers[0].LocalAddr())
	exit.mu.Lock()
	entry := exit.sessions[string(nonce)]
	exit.mu.Unlock()
	if entry == nil {
		t.Fatal("valid Init rejected")
	}
	exit.packet(init, peers[0].LocalAddr())
	exit.mu.Lock()
	same := exit.sessions[string(nonce)] == entry && len(exit.sessions) == 1
	exit.mu.Unlock()
	if !same {
		t.Fatal("retransmitted Init replaced the active session")
	}
	ack := sign(2, turnACK)
	bad := bytes.Clone(ack)
	bad[len(bad)-1] ^= 1
	exit.packet(bad, peers[1].LocalAddr())
	exit.mu.Lock()
	address := entry.addr.String()
	exit.mu.Unlock()
	if address != peers[0].LocalAddr().String() {
		t.Fatal("invalid HMAC migrated peer")
	}
	exit.packet(ack, peers[1].LocalAddr())
	exit.mu.Lock()
	address = entry.addr.String()
	exit.mu.Unlock()
	if address != peers[1].LocalAddr().String() {
		t.Fatal("authenticated peer migration failed")
	}
	entry.arq.close()
	exit.packet(init, peers[0].LocalAddr())
	exit.mu.Lock()
	defer exit.mu.Unlock()
	if len(exit.sessions) != 0 {
		t.Fatal("closed token replay accepted")
	}
}

func TestTURNCloseCancelsAllocate(t *testing.T) {
	socket, err := net.ListenPacket("udp4", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	host, port, _ := net.SplitHostPort(socket.LocalAddr().String())
	client, err := NewTURNClient(links.TURNParams{Secret: turnTestSecret, Relay: "turn:turn.example.com:" + port, DialIP: host, Exit: "192.0.2.1:34780", Key: wssTestKey})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	done := make(chan error, 1)
	go func() { _, err := client.DialContext(context.Background(), "tcp", "mail.example.org:443"); done <- err }()
	socket.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := socket.ReadFrom(make([]byte, 1200)); err != nil {
		t.Fatal(err)
	}
	client.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed allocation succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("allocation did not stop")
	}
}

func TestTURNRefreshAndTLSVerification(t *testing.T) {
	client, _, _ := turnFixture(t, "tls", false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bad := client.turnParams
	bad.SNI = "other.example.org"
	if w, err := openTURN(ctx, bad); err == nil {
		w.close()
		t.Fatal("wrong TLS name accepted")
	}
	w, err := openTURN(ctx, client.turnParams)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	if _, err := w.request(ctx, 4, []turnAttr{{0xd, turnU32(600)}}, true); err != nil {
		t.Fatal(err)
	}
	if err := w.bind(ctx); err != nil {
		t.Fatal(err)
	}
}
