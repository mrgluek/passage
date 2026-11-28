package main

import (
	bytebuf "bytes"
	"fmt"
	"os"
	"runtime/pprof"
	"sync"
	"time"
)

const (
	stallLimit = 30 * time.Second
	stallName  = "passage-stall.txt"
	stallMax   = 96 << 10 // bytes of stacks kept
	// quitWaitLimit bounds how long quitting waits for the app to stop.
	quitWaitLimit = 15 * time.Second
)

// stallGuard writes every goroutine's stack to a file when a guarded step
// runs longer than limit, so a stuck lock can be found afterwards.
type stallGuard struct {
	limit time.Duration
	path  string
	note  func(format string, args ...any)

	mu   sync.Mutex
	last time.Time
}

// watch starts timing a step; call the result when it ends.
func (g *stallGuard) watch(what string) (end func()) {
	if g == nil || g.limit <= 0 {
		return func() {}
	}
	t := time.AfterFunc(g.limit, func() { g.dump(what) })
	return func() { t.Stop() }
}

// dump runs once per stall: guarded steps that nest fire together.
func (g *stallGuard) dump(what string) {
	g.mu.Lock()
	if !g.last.IsZero() && time.Since(g.last) < g.limit {
		g.mu.Unlock()
		return
	}
	g.last = time.Now()
	g.mu.Unlock()
	var buf bytebuf.Buffer
	fmt.Fprintf(&buf, "%s: %s ran longer than %s\n\n", time.Now().UTC().Format(time.RFC3339), what, g.limit)
	pprof.Lookup("goroutine").WriteTo(&buf, 2)
	out := buf.Bytes()
	if len(out) > stallMax {
		out = append(out[:stallMax:stallMax], "\n[cut]\n"...)
	}
	msg := fmt.Sprintf("stall: %s over %s, stacks in %s", what, g.limit, stallName)
	if err := os.WriteFile(g.path, out, 0600); err != nil {
		msg = fmt.Sprintf("stall: %s over %s, stacks not saved", what, g.limit)
	}
	if g.note != nil {
		g.note("%s", msg)
	}
}

// waitDone waits for done, at most limit; false means it timed out.
func waitDone(done <-chan struct{}, limit time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(limit):
		return false
	}
}
