package main

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestReadNetworkNative reads the real network state of the machine running
// the tests and checks that it is stable. It logs what it found, so the CI
// logs show how each platform behaves.
func TestReadNetworkNative(t *testing.T) {
	start := time.Now()
	first, ok := readNetwork()
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("readNetwork took %v", elapsed)
	}
	t.Logf("ok=%v dns=%v paths=%q key=%q", ok, first.dns, first.paths, first.key())
	routes, err := defaultRoutes()
	t.Logf("default routes: %d err=%v", len(routes), err)
	for _, r := range routes {
		t.Logf("  %s index=%d gateway=%q address=%q metric=%d ipv6=%v", r.name, r.index, r.gateway, r.address, r.metric, r.ipv6)
	}
	second, ok2 := readNetwork()
	if ok != ok2 || first.key() != second.key() {
		t.Fatalf("the network key is not stable: %q (ok=%v) then %q (ok=%v)", first.key(), ok, second.key(), ok2)
	}
}

// TestWatchNetwork follows the network for PASSAGE_WATCH_SECONDS seconds (it
// is skipped without that variable) and prints when the key changes and when
// the app would restart the router. Run it by hand while plugging adapters,
// switching networks or connecting a VPN.
func TestWatchNetwork(t *testing.T) {
	seconds, _ := strconv.Atoi(os.Getenv("PASSAGE_WATCH_SECONDS"))
	if seconds <= 0 {
		t.Skip("set PASSAGE_WATCH_SECONDS to watch the network")
	}
	started := time.Now()
	state, ok := readNetwork()
	var change networkChange
	if ok {
		change = networkChange{current: state.key()}
	}
	last, restarts := state.key(), 0
	fmt.Printf("%s start ok=%v dns=%v paths=%q\n", time.Now().Format("15:04:05"), ok, state.dns, state.paths)
	for time.Since(started) < time.Duration(seconds)*time.Second {
		time.Sleep(2 * time.Second)
		state, ok = readNetwork()
		now := time.Now()
		if key := state.key(); key != last {
			last = key
			fmt.Printf("%s key changed ok=%v dns=%v paths=%q\n", now.Format("15:04:05"), ok, state.dns, state.paths)
		}
		if ok && change.observe(state.key(), now) {
			restarts++
			fmt.Printf("%s RESTART #%d\n", now.Format("15:04:05"), restarts)
		}
	}
	fmt.Printf("%s done: %d restarts in %d s\n", time.Now().Format("15:04:05"), restarts, seconds)
}
