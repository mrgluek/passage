//go:build darwin && cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMacInstanceReopenAndRelease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// A stale endpoint left by a previous process must not prevent startup.
	if err := os.WriteFile(filepath.Join(dir, "passage.lock"), []byte("/missing/open.sock"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := claimMacInstance(dir)
	if err != nil || first == nil {
		t.Fatalf("first instance: %v", err)
	}
	t.Cleanup(first.close)
	second, err := claimMacInstance(dir)
	if second != nil {
		second.close()
		t.Fatal("second launch acquired the lock")
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.open:
	case <-time.After(time.Second):
		t.Fatal("second launch did not request the window")
	}
	socketDir := first.socketDir
	first.close()
	if _, err := os.Stat(socketDir); !os.IsNotExist(err) {
		t.Fatalf("socket directory was not removed: %v", err)
	}
	next, err := claimMacInstance(dir)
	if err != nil || next == nil {
		t.Fatalf("relaunch after exit: %v", err)
	}
	next.close()
}

func TestMacInstanceConcurrentLaunches(t *testing.T) {
	dir := t.TempDir()
	const launches = 8
	type result struct {
		instance *macInstance
		err      error
	}
	results := make(chan result, launches)
	var start sync.WaitGroup
	start.Add(1)
	for range launches {
		go func() {
			start.Wait()
			instance, err := claimMacInstance(dir)
			results <- result{instance, err}
		}()
	}
	start.Done()
	owners := 0
	for range launches {
		r := <-results
		if r.err != nil {
			t.Error(r.err)
		}
		if r.instance != nil {
			owners++
			t.Cleanup(r.instance.close)
		}
	}
	if owners != 1 {
		t.Fatalf("got %d owners, want one", owners)
	}
}
