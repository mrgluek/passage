package tunnel

import (
	"net"
	"strings"
	"sync/atomic"
	"time"
)

const groupTrialWindow = 2 * time.Minute

type groupTrial struct {
	group     *group
	hold      *hold
	heldRoute Route
	route     Route
	fallback  Route
	released  atomic.Bool
}

type trialConn struct {
	net.Conn
	tunnel *Tunnel
	trial  *groupTrial
	local  net.Conn
}

func (c *trialConn) finish(stalled bool) { c.tunnel.finishTrial(c.trial, stalled) }
func (c *trialConn) Close() error {
	err := c.Conn.Close()
	c.trial.released.Store(true)
	return err
}

func (t *Tunnel) watchTrial(c net.Conn, trial *groupTrial) *trialConn {
	tc := &trialConn{tunnel: t, trial: trial}
	tc.Conn = watchResponses(c, func() { tc.finish(false) }, func(first bool) {
		if first {
			tc.finish(true)
		} else {
			t.connectionFeedback(trial.group, trial.route, "trial", true, false)
		}
		if tc.local != nil {
			tc.local.Close()
		}
	})
	return tc
}

func (t *Tunnel) hasTrialHoldLocked(g *group) bool {
	if g.kind == kindOther {
		return false
	}
	for _, r := range []Route{RouteDirect, RouteSmart} {
		if h := g.stalls[r]; h != nil && (h.state == Held || h.state == Trial) {
			return true
		}
	}
	return false
}

func (t *Tunnel) startTrialLocked(g *group, usable func(Route) bool, fallback Route) *groupTrial {
	if g.trial != nil && g.trial.released.Load() {
		g.trial = nil
	}
	if t.mode != ModeAuto || strings.HasPrefix(t.only, "route:") || g.kind == kindOther ||
		!usable(fallback) || g.trial != nil ||
		!g.lastTrial.IsZero() && t.timeNow().Sub(g.lastTrial) < 30*time.Second {
		return nil
	}
	for _, r := range []Route{RouteDirect, RouteSmart} {
		if h := g.stalls[r]; h != nil && h.state == Trial {
			route := RouteDirect
			if r == RouteSmart && g.lastResult.SmartOK && g.lastResult.RelayOK == 0 {
				route = RouteSmart
			}
			if !usable(route) || route == fallback {
				continue
			}
			trial := &groupTrial{group: g, hold: h, heldRoute: r, route: route, fallback: fallback}
			g.trial = trial
			g.lastTrial = t.timeNow()
			return trial
		}
	}
	return nil
}

func (t *Tunnel) finishTrial(trial *groupTrial, stalled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, h := trial.group, trial.hold
	defer t.rememberGroupHoldLocked(g)
	t.tickGroupLocked(g, t.timeNow())
	if g.trial != trial {
		return
	}
	g.trial = nil
	trial.released.Store(true)
	if t.mode != ModeAuto || strings.HasPrefix(t.only, "route:") || g.kind == kindOther || h.state != Trial {
		return
	}
	now := t.timeNow()
	outcome := "ok"
	if stalled {
		outcome = "stall"
	}
	t.recordConnectionLocked(g, outcome, trial.route.String())
	if stalled {
		t.stats[trial.route].Fails++
		t.stats[trial.route].Stalls++
		h.count++
		h.enterHeld(now)
		t.log.stalled(now, h, holdUnit(g, trial.heldRoute), "trial failed", h.until.Sub(now), h.count)
		if trial.route != RouteDirect {
			return
		}
		if g.failedTrials > 0 && g.goodSinceTrial && !g.probeBlind {
			g.probeBlind = true
			g.probeBlindAt = now
			t.log.add("%s", historyLine(historyTransition{prefix: "disagree", unit: g.historyUnit(), probe: "ok", outcome: "stall", route: "direct", reason: "2 trials failed, probes ignored for return"}))
		}
		g.failedTrials++
		g.goodSinceTrial = false
		return
	}
	g.failStreak = 0
	if trial.route == RouteDirect {
		for _, r := range []Route{RouteDirect, RouteSmart} {
			if h := g.stalls[r]; h != nil {
				h.clear()
			}
		}
		g.failedTrials = 0
		g.goodSinceTrial = false
		g.probeBlind = false
		g.probeBlindAt = time.Time{}
	} else {
		h.clear()
	}
	t.setRouteLocked(g, trial.route, "trial ok")
	t.log.add("%s", historyLine(historyTransition{prefix: "unblocked", unit: holdUnit(g, trial.heldRoute), reason: "trial ok"}))
	if trial.route == RouteDirect && t.cleanMemoryAllowedLocked(g) {
		rememberBlockedAt(t.cfg, g.name, false, now)
	}
}
