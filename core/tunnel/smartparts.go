package tunnel

import (
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// part is one piece of a connection's first record.
type part struct {
	b      []byte
	lowTTL bool // sent with TTL 1
	oob    bool // followed by one urgent byte
}

// partSender is what sending parts needs from a socket; tests fake it.
type partSender interface {
	write(b []byte) (int, error)
	writeOOB(b []byte) (int, error) // b and one urgent byte; counts b only
	lowTTL() (restore func(), err error)
	waitSent() // returns once written bytes have left the socket, or soon
}

// sendParts sends parts in order. Each part leaves the socket before the
// next is written, and a lowered TTL is restored only after its part left.
func sendParts(s partSender, parts []part) (int, error) {
	n := 0
	for _, p := range parts {
		var restore func()
		if p.lowTTL {
			if r, err := s.lowTTL(); err == nil {
				restore = r
			}
		}
		var m int
		var err error
		if p.oob {
			m, err = s.writeOOB(p.b)
		} else {
			m, err = s.write(p.b)
		}
		n += m
		s.waitSent()
		if restore != nil {
			restore()
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// firstRecordWait bounds how long the first record is collected when it
// comes in more than one write.
var firstRecordWait = 100 * time.Millisecond

const maxRecord = 5 + 1<<14

// partConn sends the first TLS record of a connection in the parts its plan
// gives; everything else, and data that is not a handshake record, goes as
// it is. The first record is collected whole before anything is sent.
type partConn struct {
	*net.TCPConn
	plan   func(rec []byte) []part // nil parts: send as is
	sender partSender

	mu      sync.Mutex
	pending []byte
	done    bool
	timer   *time.Timer
	err     error
}

func newPartConn(c *net.TCPConn, plan func([]byte) []part) *partConn {
	return &partConn{TCPConn: c, plan: plan, sender: tcpSender{c}}
}

// recordLen is the length of the record that starts p, 0 when p does not
// start a handshake record, or 5 when its header is not complete yet.
func recordLen(p []byte) int {
	switch {
	case len(p) == 0 || p[0] != 0x16:
		return 0
	case len(p) < 5:
		return 5
	}
	return min(5+(int(p[3])<<8|int(p[4])), maxRecord)
}

func (c *partConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	if c.done {
		err := c.err
		c.mu.Unlock()
		if err != nil {
			return 0, err
		}
		// Not under the lock: a write blocked on the peer must not hold up Close.
		return c.TCPConn.Write(b)
	}
	defer c.mu.Unlock()
	c.pending = append(c.pending, b...)
	if need := recordLen(c.pending); need > 0 && len(c.pending) < need {
		if c.timer == nil {
			c.timer = time.AfterFunc(firstRecordWait, c.flushLater)
		}
		return len(b), nil
	}
	if err := c.flushLocked(); err != nil {
		return 0, err
	}
	return len(b), nil
}

// ReadFrom keeps io.Copy on Write: the embedded TCPConn's would bypass it.
func (c *partConn) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{c}, r)
}

func (c *partConn) flushLater() {
	c.mu.Lock()
	if !c.done {
		c.err = c.flushLocked()
	}
	c.mu.Unlock()
}

func (c *partConn) flushLocked() error {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.done = true
	data := c.pending
	c.pending = nil
	if len(data) == 0 {
		return nil
	}
	rec := data
	if n := recordLen(data); n > 0 && n < len(data) {
		rec = data[:n]
	}
	parts := c.plan(rec)
	if len(parts) == 0 {
		_, err := c.TCPConn.Write(data)
		return err
	}
	if len(rec) < len(data) {
		parts = append(parts, part{b: data[len(rec):]})
	}
	_, err := sendParts(c.sender, parts)
	return err
}

func (c *partConn) CloseWrite() error {
	c.mu.Lock()
	if !c.done {
		c.flushLocked()
	}
	c.mu.Unlock()
	return c.TCPConn.CloseWrite()
}

func (c *partConn) Close() error {
	c.mu.Lock()
	if !c.done {
		c.flushLocked()
	}
	c.mu.Unlock()
	return c.TCPConn.Close()
}

// cutAt splits b at the given positions (increasing, inside b).
func cutAt(b []byte, cuts []int) []part {
	var parts []part
	prev := 0
	for _, c := range cuts {
		if c <= prev || c >= len(b) {
			continue
		}
		parts = append(parts, part{b: b[prev:c]})
		prev = c
	}
	return append(parts, part{b: b[prev:]})
}

// helloName finds the server name in a ClientHello record: its start and
// end in rec. ok is false for anything else.
func helloName(rec []byte) (start, end int, ok bool) {
	if len(rec) < 9 || rec[0] != 0x16 || rec[5] != 1 {
		return 0, 0, false
	}
	p := 9 + 2 + 32 // record header, handshake header, version, random
	if p >= len(rec) {
		return 0, 0, false
	}
	p += 1 + int(rec[p]) // session id
	if p+2 > len(rec) {
		return 0, 0, false
	}
	p += 2 + (int(rec[p])<<8 | int(rec[p+1])) // cipher suites
	if p >= len(rec) {
		return 0, 0, false
	}
	p += 1 + int(rec[p]) // compression
	if p+2 > len(rec) {
		return 0, 0, false
	}
	extEnd := min(p+2+(int(rec[p])<<8|int(rec[p+1])), len(rec))
	p += 2
	for p+4 <= extEnd {
		typ, n := int(rec[p])<<8|int(rec[p+1]), int(rec[p+2])<<8|int(rec[p+3])
		p += 4
		if typ == 0 { // server_name: list length, name type, name length, name
			if p+5 > extEnd || rec[p+2] != 0 {
				return 0, 0, false
			}
			l := int(rec[p+3])<<8 | int(rec[p+4])
			start, end = p+5, p+5+l
			if l == 0 || end > len(rec) {
				return 0, 0, false
			}
			return start, end, true
		}
		p += n
	}
	return 0, 0, false
}

// midSLD is the middle of the name's second-level label, as an offset in name.
func midSLD(name string) int {
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return len(name) / 2
	}
	sld := labels[len(labels)-2]
	return strings.LastIndex(name, sld+"."+labels[len(labels)-1]) + max(len(sld)/2, 1)
}

// sniCuts are the cuts around the server name: after the first byte, at the
// name's start, inside its second-level label and before its last byte.
func sniCuts(rec []byte) ([]int, bool) {
	start, end, ok := helloName(rec)
	if !ok {
		return nil, false
	}
	mid := start + midSLD(string(rec[start:end]))
	return []int{1, start, mid, end - 1}, true
}

// splitSNIPlan cuts the record around the server name.
func splitSNIPlan(rec []byte) []part {
	cuts, ok := sniCuts(rec)
	if !ok {
		return nil
	}
	return cutAt(rec, cuts)
}

// disorderSNIPlan cuts like splitSNIPlan and sends every other part with
// TTL 1; on Windows only the second part does.
func disorderSNIPlan(rec []byte) []part {
	parts := splitSNIPlan(rec)
	for i := range parts {
		if runtime.GOOS == "windows" {
			parts[i].lowTTL = i == 1
		} else {
			parts[i].lowTTL = i%2 == 0
		}
	}
	return parts
}

// oobSNIPlan sends the record up to the middle of the name with one urgent
// byte after it, then the rest cut before the name's last byte.
func oobSNIPlan(rec []byte) []part {
	cuts, ok := sniCuts(rec)
	if !ok {
		return nil
	}
	parts := cutAt(rec, cuts[2:])
	parts[0].oob = true
	return parts
}

// tcpSender sends parts on a real socket.
type tcpSender struct{ c *net.TCPConn }

func (s tcpSender) write(b []byte) (int, error)    { return s.c.Write(b) }
func (s tcpSender) writeOOB(b []byte) (int, error) { return sendOOB(s.c, b) }
func (s tcpSender) waitSent()                      { waitSent(s.c) }

// lowTTL sets the TTL (hop limit) to 1 and returns the restore function.
func (s tcpSender) lowTTL() (func(), error) {
	raw, err := s.c.SyscallConn()
	if err != nil {
		return nil, err
	}
	level, opt := syscall.IPPROTO_IP, syscall.IP_TTL
	if a, ok := s.c.RemoteAddr().(*net.TCPAddr); ok && a.IP.To4() == nil {
		level, opt = syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS
	}
	old := 64
	var serr error
	if err := raw.Control(func(fd uintptr) {
		if v, err := syscall.GetsockoptInt(sockfd(fd), level, opt); err == nil && v > 0 {
			old = v
		}
		serr = syscall.SetsockoptInt(sockfd(fd), level, opt, 1)
	}); err != nil {
		return nil, err
	}
	if serr != nil {
		return nil, serr
	}
	return func() {
		raw.Control(func(fd uintptr) { syscall.SetsockoptInt(sockfd(fd), level, opt, old) })
	}, nil
}
