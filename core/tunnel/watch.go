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

// A quiet period after a late write closes the connection only while it looks
// like the early freeze the rule was made for: a young connection that has
// received little. Anything older or bigger stays open.
const (
	earlyFreezeAge   = time.Minute
	earlyFreezeBytes = 32 << 10
)

func earlyFreeze(age time.Duration, received int64) bool {
	return age <= earlyFreezeAge && received <= earlyFreezeBytes
}

// Variables allow short deadlines in tests.
var firstResponseTimeout = defaultFirstResponseTimeout
var responseStallTimeout = defaultResponseStallTimeout

// responseWatch waits for a reply only after all writes have completed.
type responseWatch struct {
	net.Conn
	mu                     sync.Mutex
	timer                  *time.Timer
	first, pending, closed bool
	generation             uint64
	writing                int
	firstLimit, laterLimit time.Duration
	success                func()
	stalled                func(bool)
	// keepLate, when set, is asked at a later-reply expiry whether the
	// connection should stay open: true keeps it and waits for the next write.
	keepLate func() bool
}

func watchResponses(c net.Conn, success func(), stalled func(bool)) net.Conn {
	return watchResponsesKeep(c, success, stalled, nil)
}

// watchResponsesKeep is watchResponses where a quiet period after earlier
// replies may leave the connection open: many protocols send acknowledgements
// that get no answer, so silence after a write is not proof of a dead path.
func watchResponsesKeep(c net.Conn, success func(), stalled func(bool), keepLate func() bool) net.Conn {
	return &responseWatch{Conn: c, first: true, firstLimit: firstResponseTimeout, laterLimit: responseStallTimeout, success: success, stalled: stalled, keepLate: keepLate}
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
		if !c.first && c.keepLate != nil {
			c.mu.Unlock()
			keep := c.keepLate()
			c.mu.Lock()
			if !c.pending || c.closed || c.writing > 0 || c.generation != generation {
				c.mu.Unlock()
				return
			}
			if keep {
				c.pending = false // wait for the next write
				c.mu.Unlock()
				return
			}
		}
		c.closed = true
		first := c.first
		c.mu.Unlock()
		c.stalled(first)
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
