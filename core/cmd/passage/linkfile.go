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
		if err == nil && r.Mode == "subscribe" {
			r, err = tunnel.SubscribeLink(cfg, line, true)
		}
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
		routes := make([]string, len(cfg.Routes))
		for i, r := range cfg.Routes {
			routes[i] = fmt.Sprintf("%d %s (%s)", r.Priority, r.ID, r.Type)
		}
		return t.f("import_route_count", len(cfg.Routes), strings.Join(routes, ", "))
	}
	ipv6 := t.f("server_ipv6_off")
	if cfg.TunnelIPv6 {
		ipv6 = t.f("server_ipv6_on")
	}
	return t.f("link_ok_zone", cfg.Zone) + "\n" + ipv6
}

// routeChoice is one installed route that can be chosen (as the app's
// Settings.Route).
type routeChoice struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Priority int    `json:"priority"`
	Mode     string `json:"mode"`
	Label    string `json:"label"`
}

// installedRoutes is the routes the imported links set up, in the order the
// router tries them.
func installedRoutes(cfg tunnel.Config) []routeChoice {
	out := []routeChoice{}
	add := func(id, kind string, priority int) {
		label := fmt.Sprintf("%d %s (%s)", priority, id, kind)
		if id == kind {
			label = fmt.Sprintf("%d %s", priority, id)
		}
		for _, sub := range cfg.Subscriptions {
			if slices.Contains(sub.RouteIDs, id) {
				label += " · " + sub.DisplayName()
				break
			}
		}
		out = append(out, routeChoice{id, kind, priority, "route:" + id, label})
	}
	if len(cfg.Routes) == 0 && cfg.Zone != "" {
		add("legacy-dns", "soar", 100)
	}
	for _, r := range tunnel.Edited(cfg.Routes, cfg.RouteEdits) {
		if r.Type != "my" && slices.Contains(tunnel.RouteKinds, r.Type) {
			add(r.ID, r.Type, int(r.Priority))
		}
	}
	slices.SortStableFunc(out, func(a, b routeChoice) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// myPriority is the Go core's (tunnel/my.go).
const myPriority = 10

// savedRoutes is every installed route with its local edits, in the order
// the router tries them; routes that are off go last.
func savedRoutes(cfg tunnel.Config) []savedRoute {
	out := []savedRoute{}
	for _, r := range cfg.Routes {
		e := cfg.RouteEdits[r.ID]
		p := int(r.Priority)
		if e.Priority != nil {
			p = int(*e.Priority)
		}
		owner := ""
		for _, sub := range cfg.Subscriptions {
			if slices.Contains(sub.RouteIDs, r.ID) {
				owner = sub.DisplayName()
				break
			}
		}
		out = append(out, savedRoute{ID: r.ID, Type: r.Type, Priority: p, On: !e.Off, Subscription: owner})
	}
	slices.SortStableFunc(out, func(a, b savedRoute) int {
		if a.On != b.On {
			if a.On {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), cmp.Compare(a.ID, b.ID))
	})
	return out
}
