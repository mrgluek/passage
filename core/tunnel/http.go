package tunnel

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	links "github.com/mrgluek/passage-link/link"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"
)

const httpChunk = 3072
const httpSessionHeader = "X-Passage-Session"

// NewHTTPClient carries encrypted streams through bodyless HTTPS GET requests.
func NewHTTPClient(params links.HTTPParams) (*WSSClient, error) {
	r := links.Route{ID: "http", Type: "http", Wire: "http-v1", Params: map[string]any{"endpoint": params.Endpoint, "key": params.Key, "dial_ip": params.DialIP}}
	if _, err := links.ValidateRoute(r); err != nil {
		return nil, err
	}
	c, err := NewWSSClient(links.WSSParams{Endpoint: "wss://" + params.Endpoint[len("https://"):], Key: params.Key})
	if err != nil {
		return nil, err
	}
	c.params.Endpoint = params.Endpoint
	// Separate authentication and record keys from wss-v1.
	c.params.Key = wssKey(params.Key, nil, "http-v1")
	c.httpMode = true
	tr := c.httpClient.Transport.(*http.Transport)
	tr.DisableCompression = true
	tr.ResponseHeaderTimeout = 10 * time.Second
	if params.DialIP != "" {
		tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(params.DialIP, port))
		}
	}
	return c, nil
}
func httpRequest(ctx context.Context, client *http.Client, endpoint, token string, query url.Values) (*http.Response, error) {
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("HTTP request failed")
	}
	req.Header.Set(httpSessionHeader, token)
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("HTTP request failed")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, errors.New("HTTP request refused")
	}
	return resp, nil
}
func (c *WSSClient) dialHTTPSession(ctx context.Context) (*wssSession, error) {
	token, nonce := wssToken(c.params.Key, time.Now())
	life, cancel := context.WithCancel(c.life)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	resp, err := httpRequest(life, c.httpClient, c.params.Endpoint, token, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	local, bridge := net.Pipe()
	s := newWSSSession(c.life, nil, &wssRecords{conn: local, seal: wssAEAD(wssKey(c.params.Key, nonce, "c2s"))}, 1, nil, c.forget)
	go func() {
		defer cancel()
		defer resp.Body.Close()
		defer bridge.Close()
		s.fail(httpDownload(resp.Body, bridge))
	}()
	go func() {
		defer cancel()
		defer bridge.Close()
		buf := make([]byte, httpChunk)
		for seq := uint64(0); ; seq++ {
			n, err := bridge.Read(buf)
			if err != nil {
				return
			}
			upload, done := context.WithTimeout(life, 10*time.Second)
			response, err := httpRequest(upload, c.httpClient, c.params.Endpoint, token, url.Values{"n": {strconv.FormatUint(seq, 10)}, "d": {base64.RawURLEncoding.EncodeToString(buf[:n])}})
			if err == nil {
				var ack [1]byte
				_, readErr := io.ReadFull(response.Body, ack[:])
				response.Body.Close()
				if readErr != nil || ack[0] != 1 {
					err = errors.New("HTTP upload not acknowledged")
				}
			}
			done()
			if err != nil {
				s.fail(err)
				return
			}
		}
	}()
	go s.run(func() error {
		salt := make([]byte, wssSalt)
		if _, err := io.ReadFull(s.rec.conn, salt); err != nil {
			return err
		}
		s.rec.open = wssAEAD(wssKey(c.params.Key, slices.Concat(nonce, salt), "s2c"))
		return nil
	})
	select {
	case <-s.ready:
		return s, nil
	case <-s.done:
		cancel()
		return nil, errors.New("HTTP session refused")
	case <-ctx.Done():
		s.fail(ctx.Err())
		cancel()
		return nil, ctx.Err()
	}
}
func httpDownload(src io.Reader, dst io.Writer) error {
	var head [2]byte
	for {
		if _, err := io.ReadFull(src, head[:]); err != nil {
			return err
		}
		n := int(binary.BigEndian.Uint16(head[:]))
		if n > httpChunk {
			return errors.New("invalid HTTP chunk")
		}
		if _, err := io.CopyN(dst, src, int64(n)); err != nil {
			return err
		}
	}
}

// HTTPExit serves http-v1. Dial must enforce the deployment's destination policy.
// It returns status 1 for a policy refusal and 2 for a connection failure.
type HTTPExit struct {
	key      []byte
	path     string
	dial     func(string, uint16) (net.Conn, byte)
	mu       sync.Mutex
	sessions map[string]*httpExitSession
	used     map[string]time.Time
	closed   bool
}
type httpExitSession struct {
	conn     net.Conn
	mu       sync.Mutex
	next     uint64
	previous [32]byte
	cancel   context.CancelFunc
}

func NewHTTPExit(key []byte, path string, dial func(string, uint16) (net.Conn, byte)) (*HTTPExit, error) {
	u, err := url.Parse(path)
	if len(key) != 32 || dial == nil || err != nil || len(path) < 2 || path[0] != '/' || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "" || u.ForceQuery {
		return nil, errors.New("invalid HTTP exit configuration")
	}
	return &HTTPExit{key: wssKey(key, nil, "http-v1"), path: path, dial: dial, sessions: map[string]*httpExitSession{}, used: map[string]time.Time{}}, nil
}
func (e *HTTPExit) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for _, s := range e.sessions {
		s.cancel()
		s.conn.Close()
	}
	return nil
}
func (e *HTTPExit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-cache, private, max-age=0")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Method != http.MethodGet || r.URL.Path != e.path || r.ContentLength != 0 || len(r.TransferEncoding) > 0 || len(r.URL.RawQuery) > 5000 {
		http.NotFound(w, r)
		return
	}
	token := r.Header.Get(httpSessionHeader)
	if r.URL.RawQuery != "" {
		e.upload(w, r, token)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 40 || base64.RawURLEncoding.EncodeToString(raw) != token {
		http.NotFound(w, r)
		return
	}
	sent := time.Unix(int64(binary.BigEndian.Uint64(raw[16:24])), 0)
	mac := hmac.New(sha256.New, wssKey(e.key, nil, "auth"))
	mac.Write(raw[:24])
	if time.Since(sent).Abs() > 5*time.Minute || !hmac.Equal(mac.Sum(nil)[:wssTag], raw[24:]) {
		http.NotFound(w, r)
		return
	}
	e.mu.Lock()
	for k, t := range e.used {
		if time.Now().After(t) {
			delete(e.used, k)
		}
	}
	_, replay := e.used[token]
	if e.closed || replay || len(e.sessions) >= 128 || len(e.used) >= 4096 {
		e.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	life, cancel := context.WithCancel(r.Context())
	local, bridge := net.Pipe()
	entry := &httpExitSession{conn: bridge, cancel: cancel}
	e.sessions[token] = entry
	e.used[token] = sent.Add(5 * time.Minute)
	e.mu.Unlock()
	defer func() {
		cancel()
		local.Close()
		bridge.Close()
		e.mu.Lock()
		delete(e.sessions, token)
		e.mu.Unlock()
	}()
	salt := make([]byte, wssSalt)
	rand.Read(salt)
	nonce := raw[:16]
	rec := &wssRecords{conn: local, open: wssAEAD(wssKey(e.key, nonce, "c2s")), seal: wssAEAD(wssKey(e.key, slices.Concat(nonce, salt), "s2c"))}
	s := newWSSSession(life, nil, rec, 32, e.dial, nil)
	defer s.fail(net.ErrClosed)
	go func() {
		if _, err := local.Write(salt); err != nil {
			s.fail(err)
			return
		}
		if err := s.send(frameHello, 0, []byte{0, 32}); err != nil {
			return
		}
		timer := time.AfterFunc(10*time.Second, cancel)
		s.run(func() error {
			frame, err := s.rec.read()
			timer.Stop()
			if err != nil {
				return err
			}
			return s.handle(frame)
		})
	}()
	stop := context.AfterFunc(life, func() { local.Close(); bridge.Close() })
	defer stop()
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, httpChunk)
			n, err := bridge.Read(buf)
			if err != nil {
				return
			}
			select {
			case chunks <- buf[:n]:
			case <-life.Done():
				return
			}
		}
	}()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	rc := http.NewResponseController(w)
	for {
		var data []byte
		select {
		case b, ok := <-chunks:
			if !ok {
				return
			}
			data = b
		case <-ticker.C:
		case <-life.Done():
			return
		}
		rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		packet := binary.BigEndian.AppendUint16(nil, uint16(len(data)))
		packet = append(packet, data...)
		if _, err := w.Write(packet); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
func (e *HTTPExit) upload(w http.ResponseWriter, r *http.Request, token string) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 2 || len(q["n"]) != 1 || len(q["d"]) != 1 {
		http.NotFound(w, r)
		return
	}
	seq, err := strconv.ParseUint(q.Get("n"), 10, 64)
	data, decodeErr := base64.RawURLEncoding.DecodeString(q.Get("d"))
	if err != nil || decodeErr != nil || len(data) == 0 || len(data) > httpChunk {
		http.NotFound(w, r)
		return
	}
	e.mu.Lock()
	entry := e.sessions[token]
	e.mu.Unlock()
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	sum := sha256.Sum256(data)
	if seq != entry.next {
		// Intermediaries may repeat an acknowledged GET.
		if entry.next > 0 && seq == entry.next-1 && sum == entry.previous {
			w.Write([]byte{1})
			return
		}
		http.NotFound(w, r)
		return
	}
	entry.conn.SetWriteDeadline(time.Now().Add(4 * time.Second))
	if _, err := entry.conn.Write(data); err != nil {
		entry.cancel()
		entry.conn.Close()
		http.NotFound(w, r)
		return
	}
	entry.next++
	entry.previous = sum
	w.Write([]byte{1})
}
