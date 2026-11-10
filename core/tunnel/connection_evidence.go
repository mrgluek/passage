package tunnel

import (
	"net"
	"time"
)

const disagreementWindow = 10 * time.Minute

type connectionResult struct {
	Outcome string `json:"outcome,omitempty"`
	Route   string `json:"route,omitempty"`
}

type connectionObserverKey struct{}

func connectionRoute(c net.Conn, route Route) string {
	if v, ok := c.(viaConn); ok {
		c = v.Conn
	}
	if v, ok := c.(*poolConn); ok {
		return v.pool.routeName(v.index)
	}
	return route.String()
}

func (t *Tunnel) recordConnection(g *group, outcome, route string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordConnectionLocked(g, outcome, route)
}

func (t *Tunnel) recordConnectionLocked(g *group, outcome, route string) {
	g.lastConn = t.timeNow()
	g.lastConnResult = connectionResult{Outcome: outcome, Route: route}
	if route == RouteDirect.String() || route == RouteSmart.String() {
		g.lastDirectConn = g.lastConn
		g.lastDirectResult = g.lastConnResult
	}
	t.logDisagreementLocked(g)
}

func (t *Tunnel) logDisagreementLocked(g *group) {
	if g.lastConn.IsZero() || g.lastProbe.IsZero() || g.lastResult.Relays == 0 {
		return
	}
	gap := g.lastConn.Sub(g.lastProbe)
	if gap <= -disagreementWindow || gap >= disagreementWindow {
		return
	}
	probeOK := g.lastResult.RelayOK > 0
	conn := g.lastConnResult.Outcome
	if !(probeOK && (conn == "stall" || conn == "refused") || !probeOK && conn == "ok") {
		return
	}
	now := t.timeNow()
	if !g.lastDisagree.IsZero() && now.Sub(g.lastDisagree) < disagreementWindow {
		return
	}
	g.lastDisagree = now
	probe := "failed"
	if probeOK {
		probe = "ok"
	}
	t.log.add("%s", historyLine(historyTransition{prefix: "disagree", unit: g.historyUnit(), probe: probe, outcome: conn, route: g.lastConnResult.Route}))
}
