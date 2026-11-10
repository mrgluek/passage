package tunnel

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

const (
	holdMemory      = 24 * time.Hour
	maxSavedHold    = 30 * time.Minute
	maxNetworkHolds = 64
	maxSavedHolds   = 1024
)

type savedStep struct {
	Until time.Time `json:"until,omitzero"`
	Step  int       `json:"step"`
	At    time.Time `json:"at"`
}

type savedHold struct {
	Route           string    `json:"route,omitempty"`
	Direct          savedStep `json:"direct,omitzero"`
	Smart           savedStep `json:"smart,omitzero"`
	ProbeBlindUntil time.Time `json:"probe_blind_until,omitzero"`
	Until           time.Time `json:"until,omitzero"`
	Step            int       `json:"step,omitempty"`
	At              time.Time `json:"at,omitzero"`
}

func (s savedStep) normalize(now time.Time) savedStep {
	if s.Step <= 0 || s.Step == int(^uint(0)>>1) || s.At.IsZero() || now.Sub(s.At) >= holdMemory {
		return savedStep{}
	}
	if s.At.After(now) {
		s.At = now.UTC()
	}
	if s.Until.After(now.Add(maxSavedHold)) {
		s.Until = now.Add(maxSavedHold).UTC()
	}
	if !s.Until.After(now) {
		s.Until = time.Time{}
	}
	return s
}

func (s savedHold) normalize(now time.Time) savedHold {
	s.Direct, s.Smart = s.Direct.normalize(now), s.Smart.normalize(now)
	pool := (savedStep{s.Until, s.Step, s.At}).normalize(now)
	s.Until, s.Step, s.At = pool.Until, pool.Step, pool.At
	if !s.ProbeBlindUntil.After(now) {
		s.ProbeBlindUntil = time.Time{}
	}
	if s.ProbeBlindUntil.After(now.Add(holdMemory)) {
		s.ProbeBlindUntil = now.Add(holdMemory).UTC()
	}
	if s.Direct == (savedStep{}) && s.Smart == (savedStep{}) && s.ProbeBlindUntil.IsZero() {
		s.Route = ""
	}
	return s
}

func (s savedHold) latest() time.Time {
	at := s.At
	for _, candidate := range []time.Time{s.Direct.At, s.Smart.At, s.ProbeBlindUntil.Add(-holdMemory)} {
		if candidate.After(at) {
			at = candidate
		}
	}
	return at
}

func hashKey(key string, size int) bool {
	if len(key) != size {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil && strings.ToLower(key) == key
}

func validHoldKey(key string) bool {
	return hashKey(key, 16) || strings.HasPrefix(key, "route:") && links.ValidID(strings.TrimPrefix(key, "route:"))
}

func loadHoldsLocked(raw json.RawMessage) {
	var holds map[string]map[string]json.RawMessage
	if json.Unmarshal(raw, &holds) != nil {
		return
	}
	if knownStrategies.holds == nil {
		knownStrategies.holds = map[string]map[string]savedHold{}
	}
	for network, entries := range holds {
		if !hashKey(network, 24) {
			continue
		}
		for key, data := range entries {
			if !validHoldKey(key) {
				continue
			}
			var entry savedHold
			if json.Unmarshal(data, &entry) != nil {
				continue
			}
			if strings.HasPrefix(key, "route:") {
				if entry.Route != "" || entry.Direct != (savedStep{}) || entry.Smart != (savedStep{}) || !entry.ProbeBlindUntil.IsZero() {
					continue
				}
			} else if entry.Route != "direct" && entry.Route != "smart" && entry.Route != "tunnel" || entry.Step != 0 || !entry.At.IsZero() || !entry.Until.IsZero() {
				continue
			}
			if knownStrategies.holds[network] == nil {
				knownStrategies.holds[network] = map[string]savedHold{}
			}
			if entry.latest().After(knownStrategies.holds[network][key].latest()) {
				knownStrategies.holds[network][key] = entry
			}
		}
	}
	boundHoldsLocked()
}

func pruneHoldsLocked(now time.Time) bool {
	changed := false
	for _, entries := range knownStrategies.holds {
		for key, entry := range entries {
			normalized := entry.normalize(now)
			if normalized != entry {
				changed = true
			}
			if normalized == (savedHold{}) {
				delete(entries, key)
			} else {
				entries[key] = normalized
			}
		}
	}
	return boundHoldsLocked() || changed
}

func boundHoldsLocked() bool {
	changed := false
	total := 0
	for network, entries := range knownStrategies.holds {
		for len(entries) > maxNetworkHolds {
			delete(entries, oldestHold(entries))
			changed = true
		}
		if len(entries) == 0 {
			delete(knownStrategies.holds, network)
		} else {
			total += len(entries)
		}
	}
	for total > maxSavedHolds || len(knownStrategies.holds) > maxKnownNetworks {
		network, key := "", ""
		var oldest time.Time
		for n, entries := range knownStrategies.holds {
			k := oldestHold(entries)
			at := entries[k].latest()
			if network == "" || at.Before(oldest) || at.Equal(oldest) && n < network {
				network, key, oldest = n, k, at
			}
		}
		delete(knownStrategies.holds[network], key)
		if len(knownStrategies.holds[network]) == 0 {
			delete(knownStrategies.holds, network)
		}
		total--
		changed = true
	}
	return changed
}

func oldestHold(entries map[string]savedHold) string {
	oldest := ""
	for key, entry := range entries {
		if oldest == "" || entry.latest().Before(entries[oldest].latest()) || entry.latest().Equal(entries[oldest].latest()) && key < oldest {
			oldest = key
		}
	}
	return oldest
}

func knownHold(c Config, key string, now time.Time) savedHold {
	if c.StateFile == "" {
		return savedHold{}
	}
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	if pruneHoldsLocked(now) {
		dirtyKnownLocked(c.StateFile)
	}
	return knownStrategies.holds[networkKey(c)][key]
}

func rememberHold(c Config, key string, entry savedHold, now time.Time) {
	if c.StateFile == "" || !validHoldKey(key) {
		return
	}
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	if knownStrategies.holds == nil {
		knownStrategies.holds = map[string]map[string]savedHold{}
	}
	network := networkKey(c)
	entry = entry.normalize(now)
	changed := knownStrategies.holds[network][key] != entry
	if changed {
		if entry == (savedHold{}) {
			delete(knownStrategies.holds[network], key)
		} else {
			if knownStrategies.holds[network] == nil {
				knownStrategies.holds[network] = map[string]savedHold{}
			}
			knownStrategies.holds[network][key] = entry
		}
	}
	if pruneHoldsLocked(now) || changed {
		dirtyKnownLocked(c.StateFile)
	}
}

func saveStep(h *hold) savedStep {
	if h == nil {
		return savedStep{}
	}
	return savedStep{h.until.UTC(), h.step, h.at.UTC()}
}

func restoreStep(s savedStep, groupHold bool) *hold {
	if s == (savedStep{}) {
		return nil
	}
	h := &hold{groupHold: groupHold, step: s.Step, at: s.At, until: s.Until, stepOnly: s.Until.IsZero()}
	h.updateState()
	return h
}

func (t *Tunnel) rememberGroupHoldLocked(g *group) {
	if g == nil || g.kind == kindOther {
		return
	}
	s := savedHold{Route: g.route.String(), Direct: saveStep(g.stalls[RouteDirect]), Smart: saveStep(g.stalls[RouteSmart])}
	if g.probeBlind {
		s.ProbeBlindUntil = g.probeBlindAt.Add(holdMemory).UTC()
	}
	rememberHold(t.cfg, groupKey(g.name), s, t.timeNow())
}

func (t *Tunnel) loadGroupHoldLocked(g *group) {
	if g.memoryRestored || g.kind == kindOther {
		return
	}
	g.memoryRestored = true
	s := knownHold(t.cfg, groupKey(g.name), t.timeNow())
	g.stalls[RouteDirect], g.stalls[RouteSmart] = restoreStep(s.Direct, true), restoreStep(s.Smart, true)
	g.restoredRoute = s.Route
	if !s.ProbeBlindUntil.IsZero() {
		g.probeBlind, g.probeBlindAt = true, s.ProbeBlindUntil.Add(-holdMemory)
	}
}

func (t *Tunnel) restoreGroupHoldLocked(g *group) (Route, bool) {
	t.loadGroupHoldLocked(g)
	selected := g.restoredRoute
	g.restoredRoute = ""
	if selected == "" {
		return RouteDirect, false
	}
	for _, r := range []Route{RouteDirect, RouteSmart} {
		if h := g.stalls[r]; h != nil && h.state == Held {
			if selected == "smart" && t.usableLocked(g, RouteSmart) {
				return RouteSmart, true
			}
			if t.usableLocked(g, RouteTunnel) {
				return RouteTunnel, true
			}
		}
	}
	return RouteDirect, false
}

func (p *routePool) restoreHolds() {
	p.stalls = map[string]map[int]*hold{p.network: {}}
	for i, id := range p.ids {
		s := knownHold(p.cfg, "route:"+id, p.clock())
		if h := restoreStep(savedStep{s.Until, s.Step, s.At}, false); h != nil {
			p.stalls[p.network][i] = h
		}
	}
}

func (p *routePool) rememberHoldLocked(i int, h *hold, now time.Time) {
	if i >= len(p.ids) {
		return
	}
	s := saveStep(h)
	cfg := p.cfg
	if p.memoryConfig != nil {
		cfg = p.memoryConfig()
	}
	rememberHold(cfg, "route:"+p.ids[i], savedHold{Until: s.Until, Step: s.Step, At: s.At}, now)
}
