package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

func TestCopyStalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passage.log")
	l := openLog(path, "test")
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []tunnel.Event{
		{Time: stamp, Msg: "router started"},
		{Time: stamp.Add(time.Second), Msg: "stalled: direct (first reply), blocked for 2 min, count=1"},
		{Time: stamp.Add(2 * time.Second), Msg: "unblocked: direct (first reply)"},
	}
	l.copyStalls(events)
	l.copyStalls(events)
	l.copyStalls(events[1:])
	if lines := l.last(logKeep); len(lines) != 2 || !strings.HasPrefix(lines[0], "2026-01-01T00:00:01.000Z [") || !strings.HasSuffix(lines[1], "test unblocked: direct (first reply)") {
		t.Fatalf("lines %v", lines)
	}
	saved := openLog(path, "test").last(logKeep)
	if len(saved) != 2 {
		t.Fatalf("saved %v", saved)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Count(string(data), "\n") != 2 {
		t.Fatalf("file %q: %v", data, err)
	}
}
