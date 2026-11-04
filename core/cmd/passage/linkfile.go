package main

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

// importLinks imports each link in text, one per line, in order: later
// links add to or replace what earlier ones set up, as importing them one
// after another in the app would. Empty lines and # comments are skipped.
func importLinks(text string) (tunnel.Config, []tunnel.ImportResult, error) {
	var cfg tunnel.Config
	var results []tunnel.ImportResult
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r, err := tunnel.ImportLink(cfg, line, true)
		if err != nil {
			return tunnel.Config{}, nil, lineError{i + 1, err}
		}
		cfg = r.Config
		results = append(results, r)
	}
	return cfg, results, nil
}

type lineError struct {
	line int
	err  error
}

func (e lineError) Error() string { return fmt.Sprintf("line %d: %v", e.line, e.err) }

// linkFile is link.txt in dir. Notepad with hidden file extensions saves
// it as link.txt.txt, so that one is read when link.txt is missing.
func linkFile(dir string) string {
	path := filepath.Join(dir, linkName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if _, err := os.Stat(path + ".txt"); err == nil {
			return path + ".txt"
		}
	}
	return path
}

// fileStamp tells whether the link file changed since it was read.
type fileStamp struct {
	path string
	size int64
	mod  time.Time
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{path, fi.Size(), fi.ModTime()}
}

// insideArchive reports whether dir is where Explorer puts a program
// started from inside a zip without extracting it: a temporary folder
// named after the archive.
func insideArchive(dir, temp string) bool {
	rel, err := filepath.Rel(temp, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return strings.Contains(strings.ToLower(rel), ".zip")
}

// describeLink says what the imported links set up, like the app's
// settings screen.
func describeLink(t tr, cfg tunnel.Config) string {
	if len(cfg.Routes) > 0 {
		types := make([]string, len(cfg.Routes))
		for i, r := range cfg.Routes {
			types[i] = r.Type
		}
		return t.f("import_route_count", len(cfg.Routes), strings.Join(types, ", "))
	}
	ipv6 := t.f("server_ipv6_off")
	if cfg.TunnelIPv6 {
		ipv6 = t.f("server_ipv6_on")
	}
	return t.f("link_ok_zone", cfg.Zone) + "\n" + ipv6
}

// linkKinds is the route types the imported links and My set up, in the
// order the router tries them (as the app's Settings.routeKinds).
func linkKinds(cfg tunnel.Config, my string) []string {
	type route struct {
		priority, mine int
		id, kind       string
	}
	var routes []route
	for _, r := range cfg.Routes {
		routes = append(routes, route{int(r.Priority), 1, r.ID, r.Type})
	}
	if len(cfg.Routes) == 0 && cfg.Zone != "" {
		routes = append(routes, route{100, 1, "", "soar"})
	}
	if my != "" {
		routes = append(routes, route{myPriority, 0, "", "my"})
	}
	slices.SortStableFunc(routes, func(a, b route) int {
		return cmp.Or(cmp.Compare(a.priority, b.priority), cmp.Compare(a.mine, b.mine), cmp.Compare(a.id, b.id))
	})
	out := []string{}
	for _, r := range routes {
		if slices.Contains(tunnel.RouteKinds, r.kind) && !slices.Contains(out, r.kind) {
			out = append(out, r.kind)
		}
	}
	return out
}

// myPriority is the Go core's (tunnel/my.go).
const myPriority = 10
