package tunnel

import (
	"fmt"
	"sync"
	"time"
)

// Event is one diagnostics line. Events never carry message content; only
// routing decisions, probe results and errors.
type Event struct {
	Time time.Time `json:"t"`
	Msg  string    `json:"msg"`
}

// eventLog is a fixed-size ring of recent events for the diagnostics report.
type eventLog struct {
	mu          sync.Mutex
	buf         []Event
	next        int
	full        bool
	stallLogged map[string]time.Time
}

func newEventLog(n int) *eventLog { return &eventLog{buf: make([]Event, n)} }

func (l *eventLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf[l.next] = Event{Time: time.Now().UTC(), Msg: fmt.Sprintf(format, args...)}
	l.next = (l.next + 1) % len(l.buf)
	if l.next == 0 {
		l.full = true
	}
}

// last returns up to n most recent events, oldest first.
func (l *eventLog) last(n int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	size := l.next
	if l.full {
		size = len(l.buf)
	}
	if n > size {
		n = size
	}
	out := make([]Event, 0, n)
	for i := size - n; i < size; i++ {
		idx := i
		if l.full {
			idx = (l.next + i) % len(l.buf)
		}
		out = append(out, l.buf[idx])
	}
	return out
}

func (l *eventLog) stalled(now time.Time, route, kind string, delay time.Duration, count int64) {
	l.mu.Lock()
	if last, ok := l.stallLogged[route]; ok && now.Sub(last) < 30*time.Second {
		l.mu.Unlock()
		return
	}
	if l.stallLogged == nil {
		l.stallLogged = make(map[string]time.Time)
	}
	l.stallLogged[route] = now
	l.mu.Unlock()
	l.add("stalled: %s (%s), blocked for %d min, count=%d", route, kind, int(delay/time.Minute), count)
}
