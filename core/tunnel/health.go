package tunnel

import "time"

// Health is the one plain verdict the windows show: what the user should
// see, derived from the status only (never from hold or trial internals).
type Health struct {
	State  string    `json:"state"`            // starting, ready, working, limited, problem
	Reason string    `json:"reason,omitempty"` // no_internet, server_down, no_route, slow, reserve, waiting_app
	Since  time.Time `json:"since"`
}

const (
	healthRecent    = 5 * time.Minute
	healthStarting  = 90 * time.Second
	healthSettle    = 120 * time.Second
	healthSlowStart = 180 * time.Second
)

// health maps a status snapshot to a Health, by the first rule that fits.
func health(s Status, now time.Time) Health {
	recent := func(t time.Time) bool { return !t.IsZero() && now.Sub(t) < healthRecent }
	reserve := map[string]bool{}
	for _, r := range s.Tunnels {
		if r.Use == "reserve" {
			reserve[routeDisplayName(r.ID, r.Exit)] = true
		}
	}
	var okConns, reserveOnly, withConn, stalled int
	probeOK, anyActivity := false, false
	for _, g := range s.Groups {
		if g.LastConn != nil && g.LastConnResult != nil &&
			(recent(*g.LastConn) || g.active && g.LastConnResult.Outcome == "ok") {
			withConn++
			switch g.LastConnResult.Outcome {
			case "ok":
				okConns++
				if reserve[g.LastConnResult.Route] {
					reserveOnly++
				}
			case "stall", "later", "refused":
				stalled++
			}
			anyActivity = true
		}
		anyActivity = anyActivity || recent(g.LastProbe)
		if r := g.LastResult; !g.LastProbe.IsZero() && (r.RelayOK > 0 || r.SmartOK || r.TunnelOK) {
			probeOK = true
		}
	}
	var conns, fails int64
	for _, st := range s.Stats {
		conns += st.Conns
		fails += st.Fails
	}
	tunnelUp := false
	for _, r := range s.Tunnels {
		if r.State == routeUp && s.Stats[RouteTunnel.String()].Conns > 0 {
			tunnelUp = true
		}
	}
	allStalled := withConn > 0 && stalled == withConn
	evidence := !allStalled && (okConns > 0 || probeOK || tunnelUp)
	age := now.Sub(s.Started)
	switch {
	case s.State == "offline":
		return Health{State: "problem", Reason: "no_internet"}
	case evidence && okConns > 0 && reserveOnly == okConns:
		return Health{State: "limited", Reason: "reserve"}
	case evidence:
		return Health{State: "working"}
	case !anyActivity && conns == 0 && fails == 0:
		return Health{State: "ready", Reason: "waiting_app"}
	case age < healthStarting:
		return Health{State: "starting"}
	case s.State == "relays-down":
		return Health{State: "problem", Reason: "server_down"}
	}
	if s.State == "blocked" && s.LastResult.RelayOK == 0 && !s.LastResult.SmartOK && (s.RouteCount == 0 || allFailed(s.Tunnels)) {
		return Health{State: "problem", Reason: "no_route"}
	}
	if allStalled && age >= healthSettle {
		return Health{State: "problem", Reason: "no_route"}
	}
	if age < healthSlowStart {
		return Health{State: "starting"}
	}
	return Health{State: "problem", Reason: "slow"}
}

func allFailed(rs []RouteState) bool {
	if len(rs) == 0 {
		return false
	}
	for _, r := range rs {
		if r.State != routeFailed {
			return false
		}
	}
	return true
}
