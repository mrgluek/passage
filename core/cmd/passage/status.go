package main

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/getlantern/soar"
	"github.com/mrgluek/passage/core/tunnel"
)

// The router's status for people, as the Android app's StatusText.

// oneLine is the routes, the network and the tunnel in one line.
func oneLine(t tr, s tunnel.Status) string {
	return strings.Join(nonEmpty(routesLine(t, s), networkLine(t, s), tunnelState(t, s)), " · ")
}

// networkLine is the network as the last check saw it. Only auto checks
// the network, so with a fixed route there is nothing to show.
func networkLine(t tr, s tunnel.Status) string {
	if s.Mode != tunnel.ModeAuto {
		return ""
	}
	switch s.State {
	case "direct":
		return t.f("net_open")
	case "blocked":
		return t.f("net_restricted")
	case "relays-down":
		return t.f("net_relays_down")
	case "offline":
		return t.f("net_offline")
	}
	return t.f("net_unchecked")
}

// routesLine is "Route: direct" until relays have been seen, then "Mail:
// direct" (or "Mail: direct (2), tunnel (1)" when relays differ), plus
// "Tg: tunnel" once Telegram has been used.
func routesLine(t tr, s tunnel.Status) string {
	only := onlyType(s)
	var names []string
	counts := map[string]int{}
	for _, name := range relayNames(s) {
		g := s.Groups[name]
		n := routeName(t, g.Route, cmp.Or(g.Via, only))
		if counts[n] == 0 {
			names = append(names, n)
		}
		counts[n]++
	}
	tg, hasTg := s.Groups["telegram"]
	hasTg = hasTg && len(tg.Targets) > 0
	if len(names) == 0 && !hasTg {
		return t.f("notification_route", routeName(t, s.Route, cmp.Or(s.Via, only)))
	}
	var parts []string
	if len(names) == 1 {
		parts = append(parts, t.f("group_mail", names[0]))
	} else if len(names) > 1 {
		list := make([]string, len(names))
		for i, n := range names {
			list[i] = fmt.Sprintf("%s (%d)", n, counts[n])
		}
		parts = append(parts, t.f("group_mail", strings.Join(list, ", ")))
	}
	if hasTg {
		parts = append(parts, t.f("group_telegram", routeName(t, tg.Route, cmp.Or(tg.Via, only))))
	}
	return strings.Join(parts, " · ")
}

// onlyType is the route type a tunnel connection must use: the chosen one,
// or the only type set up.
func onlyType(s tunnel.Status) string {
	if slices.Contains(tunnel.RouteKinds, s.Mode) {
		return s.Mode
	}
	if id, ok := strings.CutPrefix(s.Mode, "route:"); ok {
		for _, r := range s.Tunnels {
			if r.ID == id {
				return r.Type
			}
		}
	}
	var types []string
	for _, r := range s.Tunnels {
		if !slices.Contains(types, r.Type) {
			types = append(types, r.Type)
		}
	}
	if len(types) == 1 {
		return types[0]
	}
	return ""
}

func relayNames(s tunnel.Status) []string {
	var out []string
	for name, g := range s.Groups {
		if g.Kind == "relay" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// tunnelState is each tunnel route's state ("Iroh: connected"), or
// "tunnel: no link".
func tunnelState(t tr, s tunnel.Status) string {
	if len(s.Tunnels) == 0 {
		return t.f("tunnel_label", t.f("tunnel_no_link"))
	}
	types := map[string]int{}
	for _, r := range s.Tunnels {
		types[r.Type]++
	}
	parts := make([]string, len(s.Tunnels))
	for i, r := range s.Tunnels {
		name := routeLabel(t, r.Type)
		if types[r.Type] > 1 {
			name = r.ID
		}
		parts[i] = t.f("tunnel_route", name, routeStateName(t, r.State))
	}
	return strings.Join(parts, ", ")
}

func routeStateName(t tr, state string) string {
	switch state {
	case "up":
		return t.f("route_state_up")
	case "failed":
		return t.f("route_state_failed")
	case "idle":
		return t.f("route_state_idle")
	}
	return state
}

// routeLabel is a route type for people: wss -> WSS.
func routeLabel(t tr, kind string) string {
	switch kind {
	case "my":
		return t.f("route_my")
	case "http":
		return "HTTP"
	case "wss":
		return "WSS"
	case "turn":
		return "TURN"
	case "iroh":
		return "Iroh"
	case "soar":
		return "Soar"
	}
	return kind
}

// routeName is the route for people; a tunnel shows the route type that
// carries it, when known.
func routeName(t tr, route, via string) string {
	switch route {
	case "direct":
		return t.f("route_direct")
	case "tunnel":
		if via == "" {
			return t.f("route_tunnel")
		}
		return routeLabel(t, via)
	case "smart":
		return t.f("route_smart")
	}
	return route
}

func stateName(t tr, state string) string {
	switch state {
	case "direct", "blocked", "offline", "unknown":
		return t.f("state_" + state)
	case "relays-down":
		return t.f("state_relays_down")
	}
	return state
}

// fullStatus is the whole status for the window.
func fullStatus(t tr, s tunnel.Status) string {
	var b strings.Builder
	line := func(format string, args ...any) { b.WriteString(t.f(format, args...) + "\n") }
	line("status_route", strings.ToUpper(routeName(t, s.Route, s.Via)), clock(s.RouteSince), s.Mode)
	line("status_network", stateName(t, s.State))
	if !s.LastProbe.IsZero() {
		r := s.LastResult
		canary := ""
		if r.CanaryChecked {
			canary = t.f("status_canary_bad")
			if r.CanaryOK {
				canary = t.f("status_canary_ok")
			}
		}
		line("status_last_check", clock(s.LastProbe), r.RelayOK, r.Relays, canary)
	}
	if relays := relayNames(s); len(relays) == 0 {
		line("status_probe_hosts", t.f("none_yet"))
	} else {
		line("status_relays")
		for _, name := range relays {
			g := s.Groups[name]
			line("status_relay", name, routeName(t, g.Route, g.Via), stateName(t, g.State))
		}
	}
	if s.Zone == "" && s.RouteCount == 0 {
		line("status_no_tunnel")
	} else {
		line("status_session", tunnelState(t, s), s.Resolvers)
		if s.TunnelIPv6 {
			line("server_ipv6_on")
		} else {
			line("server_ipv6_off")
		}
	}
	line("status_net_dns", cmp.Or(strings.Join(s.Network, ", "), t.f("none")))
	if s.DNSIntercepted {
		if s.DNSEgress != "" {
			line("status_intercepted_via", s.DNSEgress)
		} else {
			line("status_intercepted")
		}
	}
	line("status_open_conns", s.Active["direct"]+s.Active["smart"], s.Active["tunnel"])
	for _, r := range []struct{ route, label string }{{"direct", "status_direct"}, {"smart", "status_smart"}, {"tunnel", "status_tunnel"}} {
		if st, ok := s.Stats[r.route]; ok {
			line("status_route_stats", t.f(r.label), st.Conns, st.Fails, bytes(st.Up), bytes(st.Down))
		}
	}
	if len(s.Paths) > 0 {
		b.WriteString("\n" + t.f("status_paths") + "\n")
		writePaths(&b, s.Paths)
	}
	if len(s.Events) > 0 {
		b.WriteString("\n" + t.f("status_events") + "\n")
		for _, e := range s.Events {
			fmt.Fprintf(&b, "  %s %s\n", clock(e.Time), e.Msg)
		}
	}
	return b.String()
}

// writePaths is one row per resolver path, working ones first. The port
// is implied by the transport; "len" is the path's query-name cap.
func writePaths(b *strings.Builder, paths []soar.PathStat) {
	rows := slices.Clone(paths)
	working := func(p soar.PathStat) int {
		if p.Verified && !p.Suspended {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(rows, func(a, b soar.PathStat) int {
		return cmp.Or(cmp.Compare(working(b), working(a)), cmp.Compare(b.Successes, a.Successes))
	})
	w := 8
	for _, p := range rows {
		w = max(w, len(shortResolver(p.Resolver)))
	}
	format := fmt.Sprintf("%%-3s %%-4s %%-%ds %%4s %%4s %%5s %%3s\n", w)
	fmt.Fprintf(b, format, "via", "st", "resolver", "rtt", "loss", "ok", "len")
	for _, p := range rows {
		mark := "?"
		if p.Suspended {
			mark = "susp"
		} else if p.Verified {
			mark = "ok"
		}
		rtt, nameLen := "-", "-"
		if ms := p.RTT.Milliseconds(); ms > 0 {
			rtt = fmt.Sprint(ms)
		}
		if p.NameLen > 0 {
			nameLen = fmt.Sprint(p.NameLen)
		}
		fmt.Fprintf(b, format, p.Transport, mark, shortResolver(p.Resolver), rtt,
			fmt.Sprintf("%d%%", int(p.Loss*100)), fmt.Sprint(p.Successes), nameLen)
	}
}

var resolverPort = regexp.MustCompile(`^\[?(.*?)\]?:(53|853)$`)

// shortResolver drops the port, which the transport column implies.
func shortResolver(r string) string {
	if m := resolverPort.FindStringSubmatch(r); m != nil {
		return m[1]
	}
	return r
}

// diagnosis is the network test as a table: one row per target.
func diagnosis(t tr, d tunnel.Diagnosis) string {
	rows := [][]string{append([]string{t.f("test_target")}, d.Ways...)}
	for _, r := range d.Rows {
		row := []string{truncate(r.Target, 32)}
		for _, x := range r.Results {
			switch {
			case x.Skip:
				row = append(row, "-")
			case x.OK:
				row = append(row, fmt.Sprintf("ok %dms", x.MS))
			default:
				row = append(row, cmp.Or(x.Error, "fail"))
			}
		}
		rows = append(rows, row)
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				widths[i] = max(widths[i], len([]rune(cell)))
			}
		}
	}
	var b strings.Builder
	b.WriteString(t.f("test_time", clock(d.Time)) + "\n\n")
	for _, row := range rows {
		var cells []string
		for i, cell := range row {
			if i < len(widths) {
				cell += strings.Repeat(" ", widths[i]-len([]rune(cell)))
			}
			cells = append(cells, cell)
		}
		b.WriteString(strings.TrimRight(strings.Join(cells, "  "), " ") + "\n")
	}
	return b.String()
}

// diagnosisSummary is the network test in one line for the event log.
func diagnosisSummary(d tunnel.Diagnosis) string {
	ok, failed := 0, 0
	for _, r := range d.Rows {
		for _, x := range r.Results {
			switch {
			case x.Skip:
			case x.OK:
				ok++
			default:
				failed++
			}
		}
	}
	return fmt.Sprintf("%d ok, %d failed", ok, failed)
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func bytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.Local().Format("15:04:05")
}

func nonEmpty(parts ...string) []string {
	return slices.DeleteFunc(parts, func(s string) bool { return s == "" })
}
