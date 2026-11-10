package main

import (
	"fmt"
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
		{Time: stamp.Add(time.Second), Msg: "stalled: relay-1 direct (first reply), paused for 2 min, count=1"},
		{Time: stamp.Add(2 * time.Second), Msg: "resumed: relay-1 direct (first reply)"},
	}
	l.copyStalls(events)
	l.copyStalls(events)
	l.copyStalls(events[1:])
	if lines := l.last(logKeep); len(lines) != 2 || !strings.HasPrefix(lines[0], "2026-01-01T00:00:01.000Z [") || !strings.HasSuffix(lines[1], "test resumed: relay-1 direct (first reply)") {
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

func TestCopyLegacyHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passage.log")
	oldStall := "2026-01-01T00:00:00.000Z [1] test stalled: relay-1 direct (first reply), blocked for 2 min, count=1"
	oldResume := "2026-01-01T00:00:01.000Z [1] test unblocked: relay-1 direct (cooldown ended)"
	if err := os.WriteFile(path, []byte(oldStall+"\n"+oldResume+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	l := openLog(path, "test")
	event := tunnel.Event{Time: time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC), Msg: "unblocked: relay-1 direct (first reply)"}
	l.copyStalls([]tunnel.Event{event, event})
	lines := l.last(logKeep)
	if len(lines) != 3 ||
		!strings.HasSuffix(lines[0], "stalled: relay-1 direct (first reply), paused for 2 min, count=1") ||
		!strings.HasSuffix(lines[1], "resumed: relay-1 direct (cooldown ended)") ||
		!strings.HasSuffix(lines[2], "resumed: relay-1 direct (first reply)") {
		t.Fatal(lines)
	}
}

func TestCopyDisagreement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passage.log")
	l := openLog(path, "test")
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	event := tunnel.Event{Time: stamp, Msg: "disagree: relay-1 probe=ok conn=stall via route-a"}
	l.copyStalls([]tunnel.Event{event})
	l.copyStalls([]tunnel.Event{event})
	saved := openLog(path, "test").last(logKeep)
	if len(saved) != 1 || !strings.HasSuffix(saved[0], event.Msg) {
		t.Fatal(saved)
	}
}

func TestCopyStallsDistinctKeys(t *testing.T) {
	l := openLog(filepath.Join(t.TempDir(), "passage.log"), "test")
	stamp := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)
	first := tunnel.Event{Time: stamp, Msg: "stalled: relay-1 direct (first reply), paused for 2 min, count=2"}
	second := tunnel.Event{Time: stamp, Msg: "stalled: relay-2 direct (first reply), paused for 2 min, count=2"}
	backdated := tunnel.Event{Time: stamp.Add(-time.Second), Msg: "resumed: relay-1 direct (cooldown ended)"}
	l.copyStalls([]tunnel.Event{first, second, first})
	l.copyStalls([]tunnel.Event{backdated, second, backdated, {Msg: "stalled: empty timestamp"}})
	lines := openLog(l.path, "test").last(logKeep)
	if len(lines) != 3 {
		t.Fatalf("saved %v", lines)
	}
	for i, event := range []tunnel.Event{first, second, backdated} {
		if !strings.HasSuffix(lines[i], event.Msg) {
			t.Fatalf("line %d = %q, want %q", i, lines[i], event.Msg)
		}
	}
	if !strings.HasPrefix(lines[2], "2026-01-01T00:00:01.000Z [") {
		t.Fatal(lines[2])
	}
}

func TestCopyStallsBoundsKeys(t *testing.T) {
	l := openLog(filepath.Join(t.TempDir(), "passage.log"), "test")
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var events []tunnel.Event
	for i := 0; i <= logKeep; i++ {
		events = append(events, tunnel.Event{Time: stamp, Msg: fmt.Sprintf("stalled: relay-%d direct", i+1)})
	}
	l.copyStalls(events)
	if len(l.copied) != logKeep || len(l.copyOrder) != logKeep || len(l.last(logKeep+1)) != logKeep {
		t.Fatalf("keys=%d order=%d lines=%d", len(l.copied), len(l.copyOrder), len(l.last(logKeep+1)))
	}
	l.copyStalls(events[1:])
	data, err := os.ReadFile(l.path)
	if err != nil || strings.Count(string(data), "\n") != logKeep+1 {
		t.Fatalf("retained keys copied again: %v", err)
	}
	l.copyStalls(events[:1])
	data, err = os.ReadFile(l.path)
	if err != nil || strings.Count(string(data), "\n") != logKeep+2 || len(l.copied) != logKeep {
		t.Fatalf("oldest key was not evicted: %v", err)
	}
}
