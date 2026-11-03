package tunnel

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/getlantern/soar"
	links "github.com/mrgluek/passage-link/link"
)

const (
	wssMessageLimit = 65536
	wssData         = 16384 // payload bytes per DATA frame
	wssHead         = 5     // frame type and stream ID
	wssTag          = 16
	wssSalt         = 16
	wssWindow       = 256 << 10
	wssMaxSessions  = 8
)

// Frame types.
const (
	frameHello byte = iota
	frameOpen
	frameReply
	frameData
	frameFin
	frameReset
	frameWindow
)

var wssIdle = 30 * time.Second

// A session that carried this many bytes takes no new streams and closes
// after its last one, since exits may cap the work done per session.
var wssRotate int64 = 50 << 20

var (
	errWSSRecord = errors.New("invalid WSS record")
	errWSSBusy   = errors.New("WSS sessions busy")
	errWSSReset  = errors.New("WSS stream reset")
	errWSSIdle   = errors.New("WSS session idle")
	errWSSDrain  = errors.New("WSS session rotated")
)

// WSSClient opens TCP streams through a WebSocket exit, several per session.
type WSSClient struct {
	params     links.WSSParams
	httpClient *http.Client
	mu         sync.Mutex
	closed     bool
	sessions   []*wssSession
	dialing    chan struct{} // closed when the pending session dial ends
	life       context.Context
	cancel     context.CancelFunc
}

func NewWSSClient(params links.WSSParams) (*WSSClient, error) {
	r := links.Route{ID: "web", Type: "wss", Wire: "wss-v1", Params: map[string]any{"endpoint": params.Endpoint, "key": params.Key}}
	if _, err := links.ValidateRoute(r); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	// Endpoints come from private links. Never follow redirects or route these
	// connections through environment HTTP proxies.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &WSSClient{params: params, httpClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, life: life, cancel: cancel}, nil
}

func wssKey(secret, salt []byte, info string) []byte {
	key, err := hkdf.Key(sha256.New, secret, salt, "passage wss-v1 "+info, 32)
	if err != nil {
		panic(err)
	}
	return key
}
func wssAEAD(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

// wssToken returns the subprotocol that authenticates one session, and its nonce.
func wssToken(secret []byte, now time.Time) (string, []byte) {
	msg := make([]byte, 24, 24+sha256.Size)
	rand.Read(msg[:16])
	binary.BigEndian.PutUint64(msg[16:], uint64(now.Unix()))
	mac := hmac.New(sha256.New, wssKey(secret, nil, "auth"))
	mac.Write(msg)
	msg = mac.Sum(msg)[:24+wssTag]
	return base64.RawURLEncoding.EncodeToString(msg), bytes.Clone(msg[:16])
}

func (c *WSSClient) DialContext(ctx context.Context, network, target string) (net.Conn, error) {
	if network != "tcp" {
		return nil, errors.New("WSS supports TCP only")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	var n uint16
	if _, err = parsePort(port, &n); err != nil {
		return nil, err
	}
	if len(host) < 1 || len(host) > 253 {
		return nil, errors.New("invalid WSS destination")
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	handshake, cancel := context.WithTimeout(ctx, handshakeTimeout)
	stopLife := context.AfterFunc(c.life, cancel)
	defer func() { stopLife(); cancel() }()
	dest := append(binary.BigEndian.AppendUint16(nil, n), host...)
	for attempt := 0; ; attempt++ {
		s, st, err := c.reserve(handshake)
		if err == nil {
			if err = s.open(handshake, st, dest); err == nil {
				return st, nil
			}
		}
		if handshake.Err() != nil {
			return nil, handshake.Err()
		}
		if !errors.Is(err, errWSSBusy) || attempt == 2 {
			return nil, err
		}
	}
}

// reserve takes a stream slot in a live session. When all are full it dials
// one new session; concurrent callers wait for that dial instead of their own.
func (c *WSSClient) reserve(ctx context.Context) (*wssSession, *wssStream, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, nil, net.ErrClosed
		}
		for _, s := range c.sessions {
			if st := s.reserve(); st != nil {
				c.mu.Unlock()
				return s, st, nil
			}
		}
		if dialing := c.dialing; dialing != nil {
			c.mu.Unlock()
			select {
			case <-dialing:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		if len(c.sessions) >= wssMaxSessions {
			c.mu.Unlock()
			return nil, nil, errWSSBusy
		}
		dialing := make(chan struct{})
		c.dialing = dialing
		c.mu.Unlock()
		s, err := c.dialSession(ctx)
		c.mu.Lock()
		c.dialing = nil
		close(dialing)
		if err == nil && c.closed {
			err = net.ErrClosed
		}
		if err != nil {
			c.mu.Unlock()
			if s != nil {
				s.fail(err)
			}
			return nil, nil, err
		}
		c.sessions = append(c.sessions, s)
		c.mu.Unlock()
	}
}
func (c *WSSClient) dialSession(ctx context.Context) (*wssSession, error) {
	token, nonce := wssToken(c.params.Key, time.Now())
	ws, response, err := websocket.Dial(ctx, c.params.Endpoint, &websocket.DialOptions{HTTPClient: c.httpClient, Subprotocols: []string{token}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("WSS handshake failed")
	}
	if ws.Subprotocol() != token {
		ws.CloseNow()
		return nil, errors.New("WSS handshake refused")
	}
	key := c.params.Key
	s := newWSSSession(c.life, ws, &wssRecords{seal: wssAEAD(wssKey(key, nonce, "c2s"))}, 1, nil, c.forget)
	go s.run(func() error {
		salt := make([]byte, wssSalt)
		if _, err := io.ReadFull(s.rec.conn, salt); err != nil {
			return err
		}
		s.rec.open = wssAEAD(wssKey(key, slices.Concat(nonce, salt), "s2c"))
		return nil
	})
	// The server sends its stream limit right after the upgrade.
	select {
	case <-s.ready:
		return s, nil
	case <-s.done:
		return nil, errors.New("WSS session refused")
	case <-ctx.Done():
		s.fail(ctx.Err())
		return nil, ctx.Err()
	}
}
func (c *WSSClient) forget(s *wssSession) {
	c.mu.Lock()
	c.sessions = slices.DeleteFunc(c.sessions, func(x *wssSession) bool { return x == s })
	c.mu.Unlock()
}

func (c *WSSClient) Established() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sessions {
		if s.active() {
			return true
		}
	}
	return false
}
func (c *WSSClient) PathStats() []soar.PathStat { return nil }
func (c *WSSClient) Close() error {
	c.mu.Lock()
	c.closed = true
	sessions := c.sessions
	c.sessions = nil
	c.mu.Unlock()
	c.cancel()
	for _, s := range sessions {
		s.fail(net.ErrClosed)
	}
	c.httpClient.CloseIdleConnections()
	return nil
}

// wssRecords carries AES-GCM records over one WebSocket byte stream.
type wssRecords struct {
	conn     net.Conn
	open     cipher.AEAD
	readSeq  uint64
	writeMu  sync.Mutex
	seal     cipher.AEAD
	writeSeq uint64
}

func wssNonce(seq uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], seq)
	return nonce
}
func (r *wssRecords) read() ([]byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(r.conn, head[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(head[:]))
	if n < wssHead+wssTag || n > wssHead+wssData+wssTag {
		return nil, errWSSRecord
	}
	sealed := make([]byte, n)
	if _, err := io.ReadFull(r.conn, sealed); err != nil {
		return nil, err
	}
	plain, err := r.open.Open(sealed[:0], wssNonce(r.readSeq), sealed, nil)
	if err != nil {
		return nil, errWSSRecord
	}
	r.readSeq++
	return plain, nil
}
func (r *wssRecords) write(plain []byte) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	out := r.seal.Seal(make([]byte, 2, 2+len(plain)+wssTag), wssNonce(r.writeSeq), plain, nil)
	binary.BigEndian.PutUint16(out, uint16(len(out)-2))
	r.writeSeq++
	_, err := r.conn.Write(out)
	return err
}

// wssSession multiplexes streams over one WebSocket. With accept set it is
// the exit side, which tests use as a reference relay.
type wssSession struct {
	ws      *websocket.Conn
	rec     *wssRecords
	life    context.Context
	cancel  context.CancelFunc
	accept  func(host string, port uint16) (net.Conn, byte)
	onClose func(*wssSession)
	ready   chan struct{} // closed when HELLO arrives
	done    chan struct{}
	mu      sync.Mutex
	streams map[uint32]*wssStream
	limit   int
	nextID  uint32
	err     error
	idle    *time.Timer
	carried atomic.Int64 // DATA bytes both ways
}

func newWSSSession(parent context.Context, ws *websocket.Conn, rec *wssRecords, limit int, accept func(string, uint16) (net.Conn, byte), onClose func(*wssSession)) *wssSession {
	ws.SetReadLimit(wssMessageLimit)
	life, cancel := context.WithCancel(parent)
	rec.conn = websocket.NetConn(life, ws, websocket.MessageBinary)
	s := &wssSession{ws: ws, rec: rec, life: life, cancel: cancel, accept: accept, onClose: onClose, ready: make(chan struct{}), done: make(chan struct{}), streams: map[uint32]*wssStream{}, limit: limit}
	go s.keepAlive()
	return s
}
func (s *wssSession) active() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.streams) > 0 }
func (s *wssSession) reserve() *wssStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil || len(s.streams) >= s.limit || s.carried.Load() >= wssRotate {
		return nil
	}
	s.nextID++
	st := newWSSStream(s, s.nextID)
	s.streams[st.id] = st
	if s.idle != nil {
		s.idle.Stop()
	}
	return st
}
func (s *wssSession) release(id uint32) {
	s.mu.Lock()
	if _, ok := s.streams[id]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.streams, id)
	empty := len(s.streams) == 0 && s.accept == nil && s.err == nil
	drained := empty && s.carried.Load() >= wssRotate
	if empty && !drained {
		if s.idle == nil {
			s.idle = time.AfterFunc(wssIdle, func() {
				if !s.active() {
					s.fail(errWSSIdle)
				}
			})
		} else {
			s.idle.Reset(wssIdle)
		}
	}
	s.mu.Unlock()
	if drained {
		s.fail(errWSSDrain)
	}
}
func (s *wssSession) open(ctx context.Context, st *wssStream, dest []byte) error {
	if err := s.send(frameOpen, st.id, dest); err != nil {
		s.release(st.id)
		return err
	}
	select {
	case status := <-st.reply:
		if status == 0 {
			return nil
		}
		s.release(st.id)
		switch status {
		case 1:
			return errDestinationRefused
		case 2:
			return errDestinationUnreachable
		case 3:
			s.mu.Lock()
			s.limit = max(1, len(s.streams))
			s.mu.Unlock()
			return errWSSBusy
		}
		return errors.New("WSS connection failed")
	case <-ctx.Done():
		st.Close()
		return ctx.Err()
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.err
	}
}
func (s *wssSession) send(kind byte, id uint32, body []byte) error {
	frame := make([]byte, wssHead, wssHead+len(body))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:], id)
	if kind == frameData {
		s.carried.Add(int64(len(body)))
	}
	if err := s.rec.write(append(frame, body...)); err != nil {
		s.fail(err)
		return err
	}
	return nil
}
func (s *wssSession) run(prepare func() error) {
	var err error
	if prepare != nil {
		err = prepare()
	}
	for err == nil {
		var frame []byte
		if frame, err = s.rec.read(); err == nil {
			err = s.handle(frame)
		}
	}
	s.fail(err)
}
func (s *wssSession) handle(frame []byte) error {
	if len(frame) < wssHead || frame[0] > frameWindow {
		return errWSSRecord
	}
	kind, id, body := frame[0], binary.BigEndian.Uint32(frame[1:]), frame[wssHead:]
	switch kind {
	case frameHello:
		select {
		case <-s.ready:
			return errWSSRecord
		default:
		}
		if s.accept != nil || id != 0 || len(body) != 2 {
			return errWSSRecord
		}
		s.mu.Lock()
		s.limit = max(1, int(binary.BigEndian.Uint16(body)))
		s.mu.Unlock()
		close(s.ready)
		return nil
	case frameOpen:
		return s.accepted(id, body)
	}
	s.mu.Lock()
	st := s.streams[id]
	s.mu.Unlock()
	if st == nil {
		return nil // closed here; frames in flight are dropped
	}
	switch kind {
	case frameReply:
		if s.accept != nil || len(body) != 1 {
			return errWSSRecord
		}
		select {
		case st.reply <- body[0]:
		default:
			return errWSSRecord
		}
	case frameData:
		s.carried.Add(int64(len(body)))
		return st.received(body)
	case frameFin:
		st.finished()
	case frameReset:
		select {
		case st.reply <- 2:
		default:
		}
		st.reset(errWSSReset)
		s.release(id)
	case frameWindow:
		if len(body) != 4 {
			return errWSSRecord
		}
		return st.grant(int(binary.BigEndian.Uint32(body)))
	}
	return nil
}
func (s *wssSession) accepted(id uint32, dest []byte) error {
	if s.accept == nil || id == 0 || len(dest) < 3 {
		return errWSSRecord
	}
	s.mu.Lock()
	if _, ok := s.streams[id]; ok {
		s.mu.Unlock()
		return errWSSRecord
	}
	if len(s.streams) >= s.limit {
		s.mu.Unlock()
		return s.send(frameReply, id, []byte{3})
	}
	st := newWSSStream(s, id)
	s.streams[id] = st
	s.mu.Unlock()
	go s.relay(st, string(dest[2:]), binary.BigEndian.Uint16(dest))
	return nil
}
func (s *wssSession) relay(st *wssStream, host string, port uint16) {
	conn, status := s.accept(host, port)
	if err := s.send(frameReply, st.id, []byte{status}); err != nil || status != 0 {
		if conn != nil {
			conn.Close()
		}
		s.release(st.id)
		return
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := io.Copy(conn, st); err != nil {
			conn.Close()
		} else if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	if _, err := io.Copy(st, conn); err != nil {
		st.Close()
	} else {
		st.CloseWrite()
	}
	wg.Wait()
	conn.Close()
}
func (s *wssSession) fail(err error) {
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return
	}
	s.err = err
	streams := s.streams
	s.streams = map[uint32]*wssStream{}
	if s.idle != nil {
		s.idle.Stop()
	}
	s.mu.Unlock()
	close(s.done)
	s.cancel()
	s.ws.CloseNow()
	for _, st := range streams {
		st.reset(io.ErrUnexpectedEOF)
	}
	if s.onClose != nil {
		s.onClose(s)
	}
}
func (s *wssSession) keepAlive() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.life, 10*time.Second)
			err := s.ws.Ping(ctx)
			cancel()
			if err != nil {
				s.fail(err)
				return
			}
		}
	}
}

// wssStream is one TCP stream in a session, with per-stream flow control.
type wssStream struct {
	s       *wssSession
	id      uint32
	reply   chan byte
	writeMu sync.Mutex
	mu      sync.Mutex
	changed chan struct{} // closed and replaced on every state change
	buf     []byte
	credit  int // bytes the peer may still send
	unacked int // bytes read since the last window update
	window  int // bytes this side may still send
	rfin    bool
	wfin    bool
	closed  bool
	err     error
	rdl     time.Time
	wdl     time.Time
}

func newWSSStream(s *wssSession, id uint32) *wssStream {
	return &wssStream{s: s, id: id, reply: make(chan byte, 1), changed: make(chan struct{}), credit: wssWindow, window: wssWindow}
}
func (st *wssStream) wakeLocked() { close(st.changed); st.changed = make(chan struct{}) }

// waitLocked waits for a state change or the deadline; st.mu is held on entry and return.
func (st *wssStream) waitLocked(deadline time.Time) {
	ch := st.changed
	st.mu.Unlock()
	defer st.mu.Lock()
	if deadline.IsZero() {
		<-ch
		return
	}
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	}
}
func expired(deadline time.Time) bool { return !deadline.IsZero() && !time.Now().Before(deadline) }

func (st *wssStream) received(b []byte) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.rfin || len(b) == 0 || len(b) > st.credit {
		return errWSSRecord
	}
	st.credit -= len(b)
	if !st.closed {
		st.buf = append(st.buf, b...)
	}
	st.wakeLocked()
	return nil
}
func (st *wssStream) finished() {
	st.mu.Lock()
	st.rfin = true
	done := st.wfin
	st.wakeLocked()
	st.mu.Unlock()
	if done {
		st.s.release(st.id)
	}
}
func (st *wssStream) grant(n int) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if n <= 0 || st.window+n > 1<<30 {
		return errWSSRecord
	}
	st.window += n
	st.wakeLocked()
	return nil
}
func (st *wssStream) reset(err error) {
	st.mu.Lock()
	if st.err == nil {
		st.err = err
	}
	st.wakeLocked()
	st.mu.Unlock()
}

func (st *wssStream) Read(b []byte) (int, error) {
	st.mu.Lock()
	for {
		switch {
		case st.closed:
			st.mu.Unlock()
			return 0, net.ErrClosed
		case expired(st.rdl):
			st.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		case len(st.buf) > 0:
			n := copy(b, st.buf)
			if st.buf = st.buf[n:]; len(st.buf) == 0 {
				st.buf = nil
			}
			st.unacked += n
			grant := 0
			if st.unacked >= wssWindow/2 {
				grant, st.unacked = st.unacked, 0
				st.credit += grant
			}
			st.mu.Unlock()
			if grant > 0 {
				st.s.send(frameWindow, st.id, binary.BigEndian.AppendUint32(nil, uint32(grant)))
			}
			return n, nil
		case st.rfin:
			st.mu.Unlock()
			return 0, io.EOF
		case st.err != nil:
			err := st.err
			st.mu.Unlock()
			return 0, err
		}
		st.waitLocked(st.rdl)
	}
}
func (st *wssStream) Write(b []byte) (int, error) {
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	written := 0
	for len(b) > 0 {
		st.mu.Lock()
		for {
			var err error
			switch {
			case st.closed || st.wfin:
				err = net.ErrClosed
			case st.err != nil:
				err = st.err
			case expired(st.wdl):
				err = os.ErrDeadlineExceeded
			}
			if err != nil {
				st.mu.Unlock()
				return written, err
			}
			if st.window > 0 {
				break
			}
			st.waitLocked(st.wdl)
		}
		n := min(len(b), st.window, wssData)
		st.window -= n
		st.mu.Unlock()
		if err := st.s.send(frameData, st.id, b[:n]); err != nil {
			return written, err
		}
		written += n
		b = b[n:]
	}
	return written, nil
}
func (st *wssStream) CloseWrite() error {
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	st.mu.Lock()
	if st.closed || st.wfin || st.err != nil {
		st.mu.Unlock()
		return nil
	}
	st.wfin = true
	done := st.rfin
	st.mu.Unlock()
	err := st.s.send(frameFin, st.id, nil)
	if done {
		st.s.release(st.id)
	}
	return err
}
func (st *wssStream) Close() error {
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return nil
	}
	st.closed = true
	quiet := st.err != nil || st.rfin && st.wfin
	st.buf = nil
	st.wakeLocked()
	st.mu.Unlock()
	if !quiet {
		st.s.send(frameReset, st.id, nil)
	}
	st.s.release(st.id)
	return nil
}
func (st *wssStream) LocalAddr() net.Addr  { return st.s.rec.conn.LocalAddr() }
func (st *wssStream) RemoteAddr() net.Addr { return st.s.rec.conn.RemoteAddr() }
func (st *wssStream) SetDeadline(t time.Time) error {
	st.mu.Lock()
	st.rdl, st.wdl = t, t
	st.wakeLocked()
	st.mu.Unlock()
	return nil
}
func (st *wssStream) SetReadDeadline(t time.Time) error {
	st.mu.Lock()
	st.rdl = t
	st.wakeLocked()
	st.mu.Unlock()
	return nil
}
func (st *wssStream) SetWriteDeadline(t time.Time) error {
	st.mu.Lock()
	st.wdl = t
	st.wakeLocked()
	st.mu.Unlock()
	return nil
}
