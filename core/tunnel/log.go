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
	mu   sync.Mutex
	buf  []Event
	next int
	full bool
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
