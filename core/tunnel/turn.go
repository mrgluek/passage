package tunnel

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

const turnCookie uint32 = 0x2112a442
const turnACK uint32 = 0xffffffff

type turnAttr struct {
	kind  uint16
	value []byte
}

func turnU32(n uint32) []byte { return binary.BigEndian.AppendUint32(nil, n) }
func turnMessage(kind uint16, id []byte, attrs []turnAttr, key []byte) []byte {
	b := make([]byte, 20)
	binary.BigEndian.PutUint16(b, kind)
	binary.BigEndian.PutUint32(b[4:], turnCookie)
	copy(b[8:], id)
	for _, a := range attrs {
		if a.kind == 0x12 && len(a.value) == 20 && a.value[1] == 2 {
			a.value = bytes.Clone(a.value)
			for i := 0; i < 12; i++ {
				a.value[8+i] ^= id[i]
			}
		}
		b = binary.BigEndian.AppendUint16(b, a.kind)
		b = binary.BigEndian.AppendUint16(b, uint16(len(a.value)))
		b = append(b, a.value...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
	}
	if key != nil {
		binary.BigEndian.PutUint16(b[2:], uint16(len(b)-20+24))
		m := hmac.New(sha1.New, key)
		m.Write(b)
		b = binary.BigEndian.AppendUint16(b, 8)
		b = binary.BigEndian.AppendUint16(b, 20)
		b = append(b, m.Sum(nil)...)
	}
	binary.BigEndian.PutUint16(b[2:], uint16(len(b)-20))
	return b
}
func turnAttrs(b []byte) (map[uint16][]byte, error) {
	if len(b) < 20 || binary.BigEndian.Uint32(b[4:]) != turnCookie || int(binary.BigEndian.Uint16(b[2:])) != len(b)-20 {
		return nil, errors.New("invalid STUN message")
	}
	out := map[uint16][]byte{}
	for i := 20; i < len(b); {
		if i+4 > len(b) {
			return nil, io.ErrUnexpectedEOF
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		k := binary.BigEndian.Uint16(b[i:])
		i += 4
		if i+n > len(b) {
			return nil, io.ErrUnexpectedEOF
		}
		out[k] = b[i : i+n]
		i += (n + 3) &^ 3
		if i > len(b) {
			return nil, io.ErrUnexpectedEOF
		}
	}
	return out, nil
}

func turnIntegrity(b, key []byte) bool {
	if _, err := turnAttrs(b); err != nil {
		return false
	}
	for i := 20; i+4 <= len(b); {
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if binary.BigEndian.Uint16(b[i:]) == 8 {
			if n != 20 {
				return false
			}
			prefix := bytes.Clone(b[:i])
			binary.BigEndian.PutUint16(prefix[2:], uint16(i+24-20))
			m := hmac.New(sha1.New, key)
			m.Write(prefix)
			return hmac.Equal(m.Sum(nil), b[i+4:i+24])
		}
		i += 4 + (n+3)&^3
	}
	return false
}
func turnPeer(address string) []byte {
	h, p, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(p)
	ip := net.ParseIP(h)
	family := byte(2)
	if v := ip.To4(); v != nil {
		ip = v
		family = 1
	} else {
		ip = ip.To16()
	}
	b := []byte{0, family}
	b = binary.BigEndian.AppendUint16(b, uint16(port)^uint16(turnCookie>>16))
	mask := binary.BigEndian.AppendUint32(nil, turnCookie)
	mask = append(mask, make([]byte, 12)...)
	for i, v := range ip {
		b = append(b, v^mask[i])
	}
	return b
}

type turnWire struct {
	conn                             net.Conn
	wire                             *countingConn
	stream                           bool
	mu                               sync.Mutex
	pendingMu                        sync.Mutex
	pending                          map[string]chan []byte
	data                             chan []byte
	done                             chan struct{}
	once                             sync.Once
	username, password, realm, nonce string
	key                              []byte
	peer                             string
}

func (w *turnWire) close() { w.once.Do(func() { close(w.done); w.conn.Close() }) }
func (w *turnWire) write(b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	for len(b) > 0 {
		n, err := w.conn.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
		if !w.stream {
			break
		}
	}
	return nil
}
func (w *turnWire) send(data []byte) error {
	b := []byte{0x40, 0}
	b = binary.BigEndian.AppendUint16(b, uint16(len(data)))
	b = append(b, data...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return w.write(b)
}
func (w *turnWire) readLoop() {
	defer w.close()
	for {
		var b []byte
		if w.stream {
			h := make([]byte, 4)
			if _, err := io.ReadFull(w.conn, h); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint16(h[2:]))
			if h[0]&0xc0 == 0 {
				n += 16
			}
			if n > 65536 {
				return
			}
			body := make([]byte, (n+3)&^3)
			if _, err := io.ReadFull(w.conn, body); err != nil {
				return
			}
			b = append(h, body[:n]...)
		} else {
			b = make([]byte, 65536)
			n, err := w.conn.Read(b)
			if err != nil {
				return
			}
			b = b[:n]
		}
		if len(b) >= 4 && binary.BigEndian.Uint16(b) == 0x4000 {
			n := int(binary.BigEndian.Uint16(b[2:]))
			if n <= len(b)-4 && n <= 1200 {
				select {
				case w.data <- bytes.Clone(b[4 : 4+n]):
				case <-w.done:
					return
				}
			}
			continue
		}
		if _, err := turnAttrs(b); err != nil {
			continue
		}
		w.pendingMu.Lock()
		ch := w.pending[string(b[8:20])]
		w.pendingMu.Unlock()
		if ch != nil {
			select {
			case ch <- b:
			default:
			}
		}
	}
}
func (w *turnWire) request(ctx context.Context, kind uint16, attrs []turnAttr, auth bool) ([]byte, error) {
	for stale := 0; stale < 2; stale++ {
		id := make([]byte, 12)
		rand.Read(id)
		a := slices.Clone(attrs)
		var key []byte
		if auth {
			a = append(a, turnAttr{6, []byte(w.username)}, turnAttr{0x14, []byte(w.realm)}, turnAttr{0x15, []byte(w.nonce)})
			key = w.key
		}
		b := turnMessage(kind, id, a, key)
		ch := make(chan []byte, 1)
		w.pendingMu.Lock()
		w.pending[string(id)] = ch
		w.pendingMu.Unlock()
		response, err := func() ([]byte, error) {
			defer func() { w.pendingMu.Lock(); delete(w.pending, string(id)); w.pendingMu.Unlock() }()
			timer := time.NewTicker(500 * time.Millisecond)
			defer timer.Stop()
			for attempt := 0; attempt < 12; attempt++ {
				if err := w.write(b); err != nil {
					return nil, err
				}
				select {
				case r := <-ch:
					return r, nil
				case <-timer.C:
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-w.done:
					return nil, net.ErrClosed
				}
			}
			return nil, errors.New("TURN request timeout")
		}()
		if err != nil {
			return nil, err
		}
		at, err := turnAttrs(response)
		if err != nil {
			return nil, err
		}
		if binary.BigEndian.Uint16(response) == kind|0x100 {
			if auth && !turnIntegrity(response, w.key) {
				return nil, errors.New("invalid TURN response integrity")
			}
			return response, nil
		}
		code := at[9]
		if auth && len(code) >= 4 && code[2] == 4 && code[3] == 38 && len(at[0x15]) > 0 {
			w.nonce = string(at[0x15])
			continue
		}
		if !auth {
			return response, nil
		}
		return nil, errors.New("TURN request refused")
	}
	return nil, errors.New("TURN stale nonce")
}
func openTURN(ctx context.Context, p links.TURNParams) (*turnWire, error) {
	u, err := links.TURNURL(p.Relay)
	if err != nil {
		return nil, err
	}
	port := u.Port()
	if port == "" {
		port = "3478"
		if u.Scheme == "turns" {
			port = "443"
		}
	}
	network := "udp"
	if u.Scheme == "turns" || u.Query().Get("transport") == "tcp" {
		network = "tcp"
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(cmp.Or(p.DialIP, u.Hostname()), port))
	if err != nil {
		return nil, err
	}
	wire := &countingConn{Conn: conn}
	conn = wire
	if u.Scheme == "turns" {
		secure := tls.Client(conn, &tls.Config{ServerName: cmp.Or(p.SNI, u.Hostname()), MinVersion: tls.VersionTLS12, RootCAs: probeRoots})
		if err = secure.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = secure
	}
	w := &turnWire{conn: conn, wire: wire, stream: network == "tcp", pending: map[string]chan []byte{}, data: make(chan []byte, 64), done: make(chan struct{}), username: p.Username, password: p.Credential, peer: p.Exit}
	if w.username == "" {
		if p.Secret == "" {
			return nil, errors.New("turn: no secret or credentials")
		}
		w.username = strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
		m := hmac.New(sha1.New, []byte(p.Secret))
		m.Write([]byte(w.username))
		w.password = base64.StdEncoding.EncodeToString(m.Sum(nil))
	}
	go w.readLoop()
	ok := false
	defer func() {
		if !ok {
			w.close()
		}
	}()
	attrs := []turnAttr{{0x19, turnU32(17 << 24)}}
	r, err := w.request(ctx, 3, attrs, false)
	if err != nil {
		return nil, err
	}
	a, err := turnAttrs(r)
	if err != nil {
		return nil, err
	}
	code := a[9]
	if binary.BigEndian.Uint16(r) != 0x113 || len(code) < 4 || code[2] != 4 || code[3] != 1 || len(a[0x14]) == 0 || len(a[0x15]) == 0 {
		return nil, errors.New("TURN challenge refused")
	}
	w.realm, w.nonce = string(a[0x14]), string(a[0x15])
	sum := md5.Sum([]byte(w.username + ":" + w.realm + ":" + w.password))
	w.key = sum[:]
	if _, err = w.request(ctx, 3, attrs, true); err != nil {
		return nil, err
	}
	if err = w.bind(ctx); err != nil {
		return nil, err
	}
	ok = true
	return w, nil
}
func (w *turnWire) bind(ctx context.Context) error {
	_, err := w.request(ctx, 9, []turnAttr{{0xc, turnU32(0x40000000)}, {0x12, turnPeer(w.peer)}}, true)
	return err
}

// turnARQ carries the record byte stream in authenticated, bounded UDP chunks.
type turnARQ struct {
	conn                           net.Conn
	bridge                         net.Conn
	input                          chan []byte
	done                           chan struct{}
	once                           sync.Once
	nonce, token, sendKey, recvKey []byte
	send                           func([]byte) error
	cleanup                        func()
}

func newTurnARQ(key, nonce, token []byte, client bool, send func([]byte) error) *turnARQ {
	a, b := net.Pipe()
	tx, rx := "pkt-s2c", "pkt-c2s"
	if client {
		tx, rx = rx, tx
	}
	return &turnARQ{conn: a, bridge: b, input: make(chan []byte, 128), done: make(chan struct{}), nonce: bytes.Clone(nonce), token: token, sendKey: wssKey(key, nonce, tx), recvKey: wssKey(key, nonce, rx), send: send}
}
func (a *turnARQ) close() {
	a.once.Do(func() {
		close(a.done)
		a.conn.Close()
		a.bridge.Close()
		if a.cleanup != nil {
			a.cleanup()
		}
	})
}
func (a *turnARQ) valid(b []byte) bool {
	h := 29
	if len(b) > 0 && b[0] == 1 {
		h = 53
	}
	if len(b) < h+16 || len(b) > h+1024+16 || (b[0] != 1 && b[0] != 2) || !bytes.Equal(b[1:17], a.nonce) {
		return false
	}
	m := hmac.New(sha256.New, a.recvKey)
	m.Write(b[:len(b)-16])
	return hmac.Equal(m.Sum(nil)[:16], b[len(b)-16:])
}
func (a *turnARQ) receive(b []byte) {
	select {
	case a.input <- b:
	case <-a.done:
	default:
	}
}
func (a *turnARQ) run() {
	defer a.close()
	chunks := make(chan []byte)
	deliver := make(chan []byte, 64)
	go func() {
		defer a.close()
		for {
			b := make([]byte, 1024)
			n, err := a.bridge.Read(b)
			if err != nil {
				return
			}
			select {
			case chunks <- b[:n]:
			case <-a.done:
				return
			}
		}
	}()
	go func() {
		defer a.close()
		for {
			select {
			case b := <-deliver:
				if _, err := a.bridge.Write(b); err != nil {
					return
				}
			case <-a.done:
				return
			}
		}
	}()
	pending := map[uint32][]byte{}
	received := map[uint32][]byte{}
	var next, ack, base uint32
	last := time.Now()
	sent := time.Time{}
	retry := time.Now()
	backoff := 200 * time.Millisecond
	confirmed := false
	packet := func(seq uint32, payload []byte) error {
		b := []byte{2}
		if a.token != nil && !confirmed {
			b[0] = 1
			b = append(b, a.token...)
		} else {
			b = append(b, a.nonce...)
		}
		b = binary.BigEndian.AppendUint32(b, seq)
		b = binary.BigEndian.AppendUint32(b, ack)
		var mask uint32
		for i := uint32(1); i <= 32; i++ {
			if _, ok := received[ack+i]; ok {
				mask |= 1 << (i - 1)
			}
		}
		b = binary.BigEndian.AppendUint32(b, mask)
		b = append(b, payload...)
		m := hmac.New(sha256.New, a.sendKey)
		m.Write(b)
		b = append(b, m.Sum(nil)[:16]...)
		sent = time.Now()
		return a.send(b)
	}
	// An empty Init authenticates the session before the first record is sent.
	if a.token != nil {
		pending[0] = nil
		next = 1
		if packet(0, nil) != nil {
			return
		}
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		var outgoing <-chan []byte
		if next-base < 32 && next < turnACK {
			outgoing = chunks
		}
		select {
		case payload := <-outgoing:
			pending[next] = payload
			if packet(next, payload) != nil {
				return
			}
			next++
		case b := <-a.input:
			if !a.valid(b) {
				continue
			}
			last = time.Now()
			confirmed = true
			h := 17
			if b[0] == 1 {
				h = 41
			}
			seq := binary.BigEndian.Uint32(b[h:])
			peerAck := binary.BigEndian.Uint32(b[h+4:])
			mask := binary.BigEndian.Uint32(b[h+8:])
			if peerAck > next {
				continue
			}
			if peerAck > base {
				base = peerAck
				backoff = 200 * time.Millisecond
				retry = time.Now()
			}
			for n := range pending {
				if n < peerAck || n > peerAck && n-peerAck <= 32 && mask&(1<<(n-peerAck-1)) != 0 {
					delete(pending, n)
				}
			}
			if seq != turnACK {
				if seq >= ack && seq-ack < 32 {
					if _, ok := received[seq]; !ok {
						received[seq] = bytes.Clone(b[h+12 : len(b)-16])
					}
				}
				for {
					payload, ok := received[ack]
					if !ok {
						break
					}
					if len(payload) > 0 {
						select {
						case deliver <- payload:
						case <-a.done:
							return
						}
					}
					delete(received, ack)
					ack++
				}
				if packet(turnACK, nil) != nil {
					return
				}
			}
		case now := <-tick.C:
			if now.Sub(last) > 45*time.Second {
				return
			}
			if len(pending) > 0 && now.Sub(retry) >= backoff {
				for n, p := range pending {
					if packet(n, p) != nil {
						return
					}
				}
				retry = now
				backoff = min(backoff*2, time.Second)
			} else if now.Sub(sent) >= 15*time.Second {
				if packet(turnACK, nil) != nil {
					return
				}
			}
		case <-a.done:
			return
		}
	}
}

func NewTURNClient(p links.TURNParams) (*WSSClient, error) {
	r := links.Route{ID: "turn", Type: "turn", Wire: "turn-v1", Params: map[string]any{"relay": p.Relay, "exit": p.Exit, "key": p.Key, "secret": p.Secret, "username": p.Username, "credential": p.Credential, "dial_ip": p.DialIP, "sni": p.SNI}}
	if _, err := links.ValidateRoute(r); err != nil {
		return nil, err
	}
	c, err := NewWSSClient(links.WSSParams{Endpoint: "wss://example.org/", Key: p.Key})
	if err != nil {
		return nil, err
	}
	c.turnMode = true
	c.turnParams = p
	c.params.Key = wssKey(p.Key, nil, "turn-v1")
	return c, nil
}
func (c *WSSClient) dialTURNSession(ctx context.Context) (*wssSession, error) {
	ctx, cancel := context.WithCancel(ctx)
	stopClient := context.AfterFunc(c.life, cancel)
	defer func() { stopClient(); cancel() }()
	w, err := openTURN(ctx, c.turnParams)
	if err != nil {
		return nil, err
	}
	token, nonce := wssToken(c.params.Key, time.Now())
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	a := newTurnARQ(c.params.Key, nonce, raw, true, w.send)
	a.cleanup = w.close
	s := newWSSSession(c.life, nil, &wssRecords{conn: a.conn, seal: wssAEAD(wssKey(c.params.Key, nonce, "c2s"))}, 1, nil, c.forget)
	s.wire = w.wire
	stop := context.AfterFunc(s.life, a.close)
	go func() {
		defer stop()
		defer a.close()
		for {
			select {
			case b := <-w.data:
				a.receive(b)
			case <-w.done:
				return
			case <-a.done:
				return
			case <-s.life.Done():
				return
			}
		}
	}()
	go func() {
		defer a.close()
		ticker := time.NewTicker(4 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				refresh, cancel := context.WithTimeout(s.life, 10*time.Second)
				_, err := w.request(refresh, 4, []turnAttr{{0xd, turnU32(600)}}, true)
				if err == nil {
					err = w.bind(refresh)
				}
				cancel()
				if err != nil {
					return
				}
			case <-w.done:
				return
			case <-a.done:
				return
			case <-s.life.Done():
				return
			}
		}
	}()
	go a.run()
	go s.run(func() error {
		salt := make([]byte, 16)
		if _, err := io.ReadFull(a.conn, salt); err != nil {
			return err
		}
		s.rec.open = wssAEAD(wssKey(c.params.Key, slices.Concat(nonce, salt), "s2c"))
		return nil
	})
	select {
	case <-s.ready:
		return s, nil
	case <-s.done:
		return nil, errors.New("TURN session refused")
	case <-ctx.Done():
		s.fail(ctx.Err())
		return nil, ctx.Err()
	}
}

// TURNExit serves turn-v1 packets. Dial must enforce the destination policy.
type TURNExit struct {
	key      []byte
	dial     func(string, uint16) (net.Conn, byte)
	mu       sync.Mutex
	sessions map[string]*turnExitSession
	used     map[string]time.Time
	conn     net.PacketConn
	closed   bool
}
type turnExitSession struct {
	arq  *turnARQ
	addr net.Addr
}

func NewTURNExit(key []byte, dial func(string, uint16) (net.Conn, byte)) (*TURNExit, error) {
	if len(key) != 32 || dial == nil {
		return nil, errors.New("invalid TURN exit configuration")
	}
	return &TURNExit{key: wssKey(key, nil, "turn-v1"), dial: dial, sessions: map[string]*turnExitSession{}, used: map[string]time.Time{}}, nil
}
func (e *TURNExit) Close() error {
	e.mu.Lock()
	e.closed = true
	conn := e.conn
	all := make([]*turnARQ, 0, len(e.sessions))
	for _, s := range e.sessions {
		all = append(all, s.arq)
	}
	e.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	for _, a := range all {
		a.close()
	}
	return nil
}
func (e *TURNExit) Serve(conn net.PacketConn) error {
	e.mu.Lock()
	if e.closed || e.conn != nil {
		e.mu.Unlock()
		return net.ErrClosed
	}
	e.conn = conn
	e.mu.Unlock()
	defer e.Close()
	for {
		b := make([]byte, 1201)
		n, addr, err := conn.ReadFrom(b)
		if err != nil {
			return err
		}
		if n > 1200 || n < 45 {
			continue
		}
		b = b[:n]
		e.packet(b, addr)
	}
}
func (e *TURNExit) packet(b []byte, addr net.Addr) {
	if b[0] != 1 && b[0] != 2 {
		return
	}
	nonce := b[1:17]
	id := string(nonce)
	e.mu.Lock()
	entry := e.sessions[id]
	if entry == nil {
		if b[0] != 1 || len(b) < 69 || e.closed {
			e.mu.Unlock()
			return
		}
		raw := b[1:41]
		sent := time.Unix(int64(binary.BigEndian.Uint64(raw[16:24])), 0)
		m := hmac.New(sha256.New, wssKey(e.key, nil, "auth"))
		m.Write(raw[:24])
		if time.Since(sent).Abs() > 5*time.Minute || !hmac.Equal(m.Sum(nil)[:16], raw[24:]) {
			e.mu.Unlock()
			return
		}
		for k, t := range e.used {
			if time.Now().After(t) {
				delete(e.used, k)
			}
		}
		if _, ok := e.used[id]; ok || len(e.sessions) >= 128 || len(e.used) >= 4096 {
			e.mu.Unlock()
			return
		}
		entry = &turnExitSession{addr: addr}
		a := newTurnARQ(e.key, nonce, nil, false, func(data []byte) error {
			e.mu.Lock()
			peer := entry.addr
			e.mu.Unlock()
			_, err := e.conn.WriteTo(data, peer)
			return err
		})
		entry.arq = a
		if !a.valid(b) {
			e.mu.Unlock()
			a.close()
			return
		}
		e.sessions[id] = entry
		e.used[id] = sent.Add(5 * time.Minute)
		a.cleanup = func() { e.mu.Lock(); delete(e.sessions, id); e.mu.Unlock() }
		go a.run()
		go e.session(a)
	}
	if entry.arq.valid(b) {
		entry.addr = addr
		entry.arq.receive(b)
	}
	e.mu.Unlock()
}
func (e *TURNExit) session(a *turnARQ) {
	defer a.close()
	salt := make([]byte, 16)
	rand.Read(salt)
	rec := &wssRecords{conn: a.conn, open: wssAEAD(wssKey(e.key, a.nonce, "c2s")), seal: wssAEAD(wssKey(e.key, slices.Concat(a.nonce, salt), "s2c"))}
	s := newWSSSession(context.Background(), nil, rec, 32, e.dial, nil)
	stop := context.AfterFunc(s.life, a.close)
	defer stop()
	defer s.fail(net.ErrClosed)
	if _, err := a.conn.Write(salt); err != nil {
		return
	}
	if err := s.send(frameHello, 0, []byte{0, 32}); err != nil {
		return
	}
	s.run(func() error {
		frame, err := rec.read()
		if err != nil {
			return err
		}
		return s.handle(frame)
	})
}
