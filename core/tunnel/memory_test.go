package tunnel

import (
	"runtime/debug"
	"testing"
	"time"
)

func TestReadMemoryReportsTheRuntime(t *testing.T) {
	m := readMemory()
	if m.Total == 0 || m.Heap == 0 || m.HeapGoal == 0 || m.Goroutines == 0 {
		t.Fatalf("empty reading: %+v", m)
	}
	if m.Heap > m.Total {
		t.Fatalf("heap %d is larger than the total %d", m.Heap, m.Total)
	}
}

func TestTuneMemorySetsASoftLimitUnlessTheEnvironmentDoes(t *testing.T) {
	old := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(old)
	t.Setenv("GOMEMLIMIT", "")
	debug.SetMemoryLimit(1 << 40)
	tuneMemory()
	if got := debug.SetMemoryLimit(-1); got != memoryLimit {
		t.Fatalf("limit %d, want %d", got, memoryLimit)
	}
	t.Setenv("GOMEMLIMIT", "512MiB")
	debug.SetMemoryLimit(1 << 40)
	tuneMemory()
	if got := debug.SetMemoryLimit(-1); got != 1<<40 {
		t.Fatalf("limit changed to %d although GOMEMLIMIT is set", got)
	}
}

func TestIdleReleaseWaitsAndSkipsABusyTunnel(t *testing.T) {
	tt := &Tunnel{conns: map[*tracked]struct{}{}}
	recent := time.Now()
	last := recent
	tt.releaseMemoryIfIdle(&last)
	if !last.Equal(recent) {
		t.Fatal("released before the interval passed")
	}
	old := time.Now().Add(-2 * releaseEvery)
	last = old
	tt.conns[&tracked{}] = struct{}{}
	tt.releaseMemoryIfIdle(&last)
	if !last.Equal(old) {
		t.Fatal("released while a connection was open")
	}
	delete(tt.conns, nil)
	tt.conns = map[*tracked]struct{}{}
	tt.releaseMemoryIfIdle(&last)
	if !last.After(old) {
		t.Fatal("did not release an idle tunnel after the interval")
	}
}
