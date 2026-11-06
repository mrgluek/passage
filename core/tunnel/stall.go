package tunnel

import (
	"net"
	"sync"
	"time"
)

const (
	defaultFirstResponseTimeout = 8 * time.Second
	defaultResponseStallTimeout = 20 * time.Second
)

// Variables allow short deadlines in tests.
var firstResponseTimeout = defaultFirstResponseTimeout
var responseStallTimeout = defaultResponseStallTimeout

type stallState struct {
	Until time.Time
	Steps int
	Count int64
}

func (s *stallState) fail(now time.Time) {
	delays := [...]time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute}
	s.Steps++
	s.Count++
	s.Until = now.Add(delays[min(s.Steps, len(delays))-1])
}
func (s *stallState) success() { s.Steps = 0; s.Until = time.Time{} }

// responseWatch waits for a reply only after all writes have completed.
type responseWatch struct {
	net.Conn
	mu                     sync.Mutex
	timer                  *time.Timer
	first, pending, closed bool
	generation             uint64
	writing                int
	firstLimit, laterLimit time.Duration
	success, stalled       func()
}

func watchResponses(c net.Conn, success, stalled func()) net.Conn {
	return &responseWatch{Conn: c, first: true, firstLimit: firstResponseTimeout, laterLimit: responseStallTimeout, success: success, stalled: stalled}
}
func (c *responseWatch) stopTimerLocked() {
	c.generation++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}
func (c *responseWatch) armLocked() {
	c.stopTimerLocked()
	if c.closed || !c.pending || c.writing > 0 {
		return
	}
	generation := c.generation
	limit := c.laterLimit
	if c.first {
		limit = c.firstLimit
	}
	c.timer = time.AfterFunc(limit, func() {
		c.mu.Lock()
		if !c.pending || c.closed || c.writing > 0 || c.generation != generation {
			c.mu.Unlock()
			return
		}
		c.closed = true
		c.mu.Unlock()
		c.stalled()
		c.Conn.Close()
	})
}
func (c *responseWatch) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return c.Conn.Write(b)
	}
	c.mu.Lock()
	c.writing++
	c.stopTimerLocked()
	c.mu.Unlock()
	n, err := c.Conn.Write(b)
	c.mu.Lock()
	c.writing--
	if n > 0 && err == nil {
		c.pending = true
	}
	if c.writing == 0 {
		c.armLocked()
	}
	c.mu.Unlock()
	return n, err
}
func (c *responseWatch) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.mu.Lock()
		if !c.closed {
			first := c.first
			c.first, c.pending = false, false
			c.stopTimerLocked()
			// Serialize the first byte with expiry and subsequent writes.
			if first && c.success != nil {
				c.success()
			}
		}
		c.mu.Unlock()
	}
	return n, err
}
func (c *responseWatch) Close() error {
	c.mu.Lock()
	c.closed = true
	c.stopTimerLocked()
	c.mu.Unlock()
	return c.Conn.Close()
}
func (c *responseWatch) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}
