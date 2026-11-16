package tunnel

import (
	"os"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

const (
	// memoryLimit is a soft limit for the Go runtime: the collector works
	// harder only when the process nears it, so a small live heap is not
	// affected. A GOMEMLIMIT set by the user is left alone.
	memoryLimit = 128 << 20
	// releaseEvery is how often an idle tunnel hands freed memory back to the
	// system, so the process does not stay large after a burst of traffic.
	releaseEvery = 5 * time.Minute
)

// tuneMemory sets the soft memory limit unless the environment already does.
func tuneMemory() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(memoryLimit)
	}
}

// MemoryStatus is the Go runtime's own account of its memory, in bytes.
type MemoryStatus struct {
	Total      uint64 `json:"total"`     // all memory mapped by the runtime
	Heap       uint64 `json:"heap"`      // heap objects, live and not yet swept
	HeapGoal   uint64 `json:"heap_goal"` // heap size the collector aims for
	Goroutines uint64 `json:"goroutines"`
	GCCycles   uint64 `json:"gc_cycles"`
}

// readMemory reads the runtime metrics, which do not stop the program.
func readMemory() MemoryStatus {
	samples := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/gc/heap/goal:bytes"},
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/gc/cycles/total:gc-cycles"},
	}
	metrics.Read(samples)
	value := func(i int) uint64 {
		if samples[i].Value.Kind() == metrics.KindUint64 {
			return samples[i].Value.Uint64()
		}
		return 0
	}
	return MemoryStatus{Total: value(0), Heap: value(1), HeapGoal: value(2), Goroutines: value(3), GCCycles: value(4)}
}

// releaseMemoryIfIdle returns freed memory to the system when no connection is
// open and releaseEvery has passed since the last time.
func (t *Tunnel) releaseMemoryIfIdle(last *time.Time) {
	now := time.Now()
	if now.Sub(*last) < releaseEvery {
		return
	}
	t.mu.Lock()
	busy := len(t.conns) > 0
	t.mu.Unlock()
	if busy {
		return
	}
	*last = now
	debug.FreeOSMemory()
}
