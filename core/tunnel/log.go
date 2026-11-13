package tunnel

import (
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/mrgluek/passage/core/internal/reporttext"
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
	stallLogged map[*hold]time.Time
	now         func() time.Time
}

func newEventLog(n int, clock ...func() time.Time) *eventLog {
	now := time.Now
	if len(clock) > 0 {
		now = clock[0]
	}
	return &eventLog{buf: make([]Event, n), now: now}
}

func (l *eventLog) add(format string, args ...any) {
	l.addAt(l.now(), format, args...)
}

func (l *eventLog) addAt(now time.Time, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf[l.next] = Event{Time: now.UTC(), Msg: reporttext.LocalEndpoints(reporttext.Secrets(fmt.Sprintf(format, args...)))}
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

func (l *eventLog) stalled(now time.Time, identity *hold, route, kind string, delay time.Duration, count int64) {
	l.mu.Lock()
	if last, ok := l.stallLogged[identity]; ok && now.Sub(last) < 30*time.Second {
		l.mu.Unlock()
		return
	}
	if l.stallLogged == nil {
		l.stallLogged = make(map[*hold]time.Time)
	}
	maps.DeleteFunc(l.stallLogged, func(_ *hold, at time.Time) bool { return now.Sub(at) >= 30*time.Second })
	l.stallLogged[identity] = now
	l.mu.Unlock()
	l.addAt(now, "%s", historyLine(historyTransition{prefix: "stalled", unit: route, reason: kind, delay: delay, count: count}))
}

type historyTransition struct {
	prefix, unit, reason  string
	delay                 time.Duration
	count                 int64
	probe, outcome, route string
}

func historyLine(e historyTransition) string {
	switch e.prefix {
	case "stalled":
		return fmt.Sprintf("stalled: %s (%s), paused for %d min, count=%d", e.unit, e.reason, int(e.delay/time.Minute), e.count)
	case "restarted":
		return fmt.Sprintf("restarted: %s (%s)", e.unit, e.reason)
	case "trial skipped":
		return fmt.Sprintf("%s: trial skipped, no new connection", e.unit)
	case "unblocked":
		return fmt.Sprintf("resumed: %s (%s)", e.unit, e.reason)
	case "disagree":
		line := fmt.Sprintf("disagree: %s probe=%s conn=%s via %s", e.unit, e.probe, e.outcome, e.route)
		if e.reason != "" {
			line += ", " + e.reason
		}
		return line
	default:
		panic("unknown history transition")
	}
}
