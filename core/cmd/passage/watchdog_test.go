package main

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStallGuardDumpsOncePerStall(t *testing.T) {
	a := newApp(t.TempDir(), "test", "", "en", &fakePlatform{})
	a.stall.limit = 50 * time.Millisecond
	a.mu.Lock() // another goroutine is stuck holding the lock
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); a.tick() }()
	time.Sleep(300 * time.Millisecond)
	a.mu.Unlock()
	wg.Wait()
	b, err := os.ReadFile(a.path(stallName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "goroutine ") || !strings.Contains(string(b), "tick ran longer") {
		t.Fatalf("no stacks written: %.200s", b)
	}
	if n := strings.Count(a.log.text(100), "stall: tick over"); n != 1 {
		t.Fatalf("%d stall lines, want 1:\n%s", n, a.log.text(100))
	}
}

func TestStallGuardQuietWhenFast(t *testing.T) {
	a := newApp(t.TempDir(), "test", "", "en", &fakePlatform{})
	a.stall.limit = 200 * time.Millisecond
	a.tick()
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(a.path(stallName)); err == nil {
		t.Fatal("stacks written for a fast tick")
	}
}

func TestStallDumpIsCapped(t *testing.T) {
	g := &stallGuard{limit: time.Millisecond, path: t.TempDir() + "/s.txt"}
	var stop sync.WaitGroup
	quit := make(chan struct{})
	for i := 0; i < 2000; i++ {
		stop.Add(1)
		go func() { defer stop.Done(); <-quit }()
	}
	g.dump("test")
	close(quit)
	stop.Wait()
	fi, err := os.Stat(g.path)
	if err != nil || fi.Size() > stallMax+64 {
		t.Fatalf("size %v err %v, want at most %d", fi, err, stallMax)
	}
}

func TestWaitDone(t *testing.T) {
	done := make(chan struct{})
	if waitDone(done, 20*time.Millisecond) {
		t.Fatal("reported done")
	}
	close(done)
	if !waitDone(done, time.Second) {
		t.Fatal("missed done")
	}
}
