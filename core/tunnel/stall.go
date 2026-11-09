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
var stallCorroborateWindow = 120 * time.Second

type stallState struct {
	Until  time.Time
	Steps  int
	Count  int64
	recent []struct {
		key string
		at  time.Time
	}
}

func (s *stallState) fail(now time.Time) {
	delays := [...]time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute}
	s.Steps++
	s.Until = now.Add(delays[min(s.Steps, len(delays))-1])
}
func (s *stallState) success() { s.Steps = 0; s.Until = time.Time{}; s.recent = nil }

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
}

func watchResponses(c net.Conn, success func(), stalled func(bool)) net.Conn {
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

func (s *stallState) blocked(now time.Time, log *eventLog, route string) bool {
	if s == nil || s.Until.IsZero() {
		return false
	}
	if now.Before(s.Until) {
		return true
	}
	s.Until = time.Time{}
	if log != nil {
		log.add("unblocked: %s (cooldown ended)", route)
	}
	return false
}
func (s *stallState) recovered(log *eventLog, route string) {
	if !s.Until.IsZero() || s.Steps > 0 {
		if log != nil {
			log.add("unblocked: %s (first reply)", route)
		}
	}
	s.success()
}
func (s *stallState) record(now time.Time, log *eventLog, route string, first bool, key string, pinned ...bool) bool {
	s.Count++
	if len(pinned) > 0 && pinned[0] {
		route += " (pinned)"
	}
	if !first {
		if log != nil {
			log.add("%s: connection closed, no reply (later)", route)
		}
		return false
	}
	recent := s.recent[:0]
	duplicate := false
	for _, stall := range s.recent {
		if now.Sub(stall.at) <= stallCorroborateWindow {
			recent = append(recent, stall)
			duplicate = duplicate || stall.key == key
		}
	}
	clear(s.recent[len(recent):])
	s.recent = recent
	if !duplicate {
		s.recent = append(s.recent, struct {
			key string
			at  time.Time
		}{key, now})
	}
	if duplicate || len(s.recent) < 2 {
		if log != nil {
			log.add("%s: connection closed, no reply (first, single)", route)
		}
		return false
	}
	if len(pinned) > 0 && pinned[0] {
		if log != nil {
			log.add("%s: connection closed, no reply (first, pinned)", route)
		}
		return true
	}
	s.fail(now)
	if log != nil {
		log.stalled(now, route, "first reply", s.Until.Sub(now), s.Count)
	}
	return true
}
