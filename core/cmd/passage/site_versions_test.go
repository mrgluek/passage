package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/mrgluek/passage/core/tunnel"
)

// The website tells people which Passage version a link needs, from a table in
// site/index.html. Every route type the core knows must have a row there, or a
// link with only that type would be shown with too old a version.
func TestSiteListsEveryRouteTypeVersion(t *testing.T) {
	src, err := os.ReadFile("../../../site/index.html")
	if err != nil {
		t.Skip("site sources not available:", err)
	}
	m := regexp.MustCompile(`var TYPES = \{([^}]*)\}`).FindSubmatch(src)
	if m == nil {
		t.Fatal("route type table not found in site/index.html")
	}
	have := map[string]bool{}
	for _, q := range regexp.MustCompile(`([a-z]+):\s*"[0-9.]+"`).FindAllSubmatch(m[1], -1) {
		have[string(q[1])] = true
	}
	for _, kind := range tunnel.RouteKinds {
		if kind != "my" && !have[kind] {
			t.Errorf("site/index.html has no minimum version for route type %q", kind)
		}
	}
}
