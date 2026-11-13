package main

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

type channelDisplay struct {
	shown          map[string]time.Time
	value, pending string
	since          time.Time
}

// channelLines also returns when each visible group first appeared.
func channelLines(t tr, s tunnel.Status, now time.Time, previous map[string]time.Time) (string, map[string]time.Time) {
	shown := map[string]time.Time{}
	kinds := map[string][]string{}
	names := make([]string, 0, len(s.Groups))
	for name := range s.Groups {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		g := s.Groups[name]
		first, kept := previous[name]
		used := g.LastConn != nil && !g.LastConn.IsZero()
		visible := used && now.Sub(*g.LastConn) <= 15*time.Minute
		if g.Kind == "telegram" {
			visible = used || len(g.Targets) > 0
		}
		if kept && now.Sub(first) < 5*time.Minute {
			visible = true
		}
		if !visible {
			continue
		}
		if !kept {
			first = now
		}
		shown[name] = first
		route := channelRoute(t, s.Mode, g.Route, g.RouteID, cmp.Or(g.Via, onlyType(s)))
		if !slices.Contains(kinds[g.Kind], route) {
			kinds[g.Kind] = append(kinds[g.Kind], route)
		}
	}
	var parts []string
	for _, kind := range []string{"relay", "telegram", "other"} {
		if len(kinds[kind]) == 0 {
			continue
		}
		key := map[string]string{"relay": "group_mail", "telegram": "group_telegram", "other": "group_other"}[kind]
		parts = append(parts, t.f(key, strings.Join(kinds[kind], "/")))
	}
	if len(parts) == 0 {
		return t.f("notification_route", channelRoute(t, s.Mode, s.Route, "", cmp.Or(s.Via, onlyType(s)))), shown
	}
	return strings.Join(parts, ", "), shown
}

func channelRoute(t tr, mode, route, id, via string) string {
	switch route {
	case "direct":
		return t.f("route_direct")
	case "smart":
		return t.f("channel_smart")
	}
	if pinned, ok := strings.CutPrefix(mode, "route:"); ok {
		return pinned
	}
	return cmp.Or(id, via, route)
}

func (d *channelDisplay) sample(t tr, s tunnel.Status, now time.Time) string {
	next, shown := channelLines(t, s, now, d.shown)
	d.shown = shown
	if d.value == "" {
		d.value = next
	}
	if next != d.pending {
		d.pending, d.since = next, now
	}
	if now.Sub(d.since) >= 10*time.Second {
		d.value = next
	}
	return d.value
}
