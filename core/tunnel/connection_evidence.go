package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

const disagreementWindow = 10 * time.Minute

type connectionResult struct {
	Outcome string `json:"outcome,omitempty"`
	Route   string `json:"route,omitempty"`
	// AfterSecs is how long a timed-out dial waited.
	AfterSecs int `json:"after_s,omitempty"`
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
	t.recordConnectionAfterLocked(g, outcome, route, 0)
}

// recordFailedDial records a dial that failed: "timeout" with the time it
// waited when the error is a timeout, "refused" for any other failure.
func (t *Tunnel) recordFailedDial(g *group, route string, err error, waited time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if isDialTimeout(err) {
		t.recordConnectionAfterLocked(g, "timeout", route, waited)
		return
	}
	t.recordConnectionLocked(g, "refused", route)
}

func isDialTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()
}

func (t *Tunnel) recordConnectionAfterLocked(g *group, outcome, route string, after time.Duration) {
	g.lastConn = t.timeNow()
	g.lastConnResult = connectionResult{Outcome: outcome, Route: route, AfterSecs: int(after.Round(time.Second) / time.Second)}
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
	if !(probeOK && (conn == "stall" || conn == "refused" || conn == "timeout") || !probeOK && conn == "ok") {
		return
	}
	now := t.timeNow()
	if !g.lastDisagree.IsZero() && now.Sub(g.lastDisagree) < disagreementWindow {
		return
	}
	g.lastDisagree = now
	if conn == "timeout" && g.lastConnResult.AfterSecs > 0 {
		conn = fmt.Sprintf("timeout after %ds", g.lastConnResult.AfterSecs)
	}
	probe := "failed"
	if probeOK {
		probe = "ok"
	}
	t.log.add("%s", historyLine(historyTransition{prefix: "disagree", unit: g.historyUnit(), probe: probe, outcome: conn, route: g.lastConnResult.Route}))
}
