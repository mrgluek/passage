package main

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/mrgluek/passage/core/tunnel"
)

// The Android app keeps its own copy of the route types that can be chosen.
// It must not drift from the core: a type missing there is silently hidden
// from the route menu.
func TestAndroidRouteKindsMatchCore(t *testing.T) {
	src, err := os.ReadFile("../../../android/app/src/main/java/info/gluek/passage/Settings.kt")
	if err != nil {
		t.Skip("Android sources not available:", err)
	}
	m := regexp.MustCompile(`ROUTE_KINDS\s*=\s*listOf\(([^)]*)\)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("ROUTE_KINDS not found in Settings.kt")
	}
	var got []string
	for _, q := range regexp.MustCompile(`"([a-z]+)"`).FindAllSubmatch(m[1], -1) {
		got = append(got, string(q[1]))
	}
	want := slices.Clone(tunnel.RouteKinds)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("Android ROUTE_KINDS %v differ from tunnel.RouteKinds %v", got, want)
	}
}
