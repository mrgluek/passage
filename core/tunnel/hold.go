package tunnel

import "time"

var stallCorroborateWindow = 120 * time.Second

type holdState uint8

const (
	Open holdState = iota
	Suspect
	Held
	Trial
)

type hold struct {
	groupHold  bool
	stepOnly   bool
	at         time.Time
	state      holdState
	until      time.Time
	trialUntil time.Time
	step       int
	count      int64
	points     []struct {
		key string
		at  time.Time
	}
}

func (s *hold) enterHeld(now time.Time) {
	if s.trialUntil.IsZero() && !s.at.IsZero() && now.Sub(s.at) >= holdMemory {
		s.step = 0
	}
	s.step++
	s.holdForStep(now)
}

func (s *hold) holdForStep(now time.Time) {
	delays := [...]time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute}
	s.at = now
	s.trialUntil = time.Time{}
	s.stepOnly = false
	s.until = now.Add(delays[min(s.step, len(delays))-1])
	s.state = Held
}
func (s *hold) clear() {
	s.step = 0
	s.at = time.Time{}
	s.stepOnly = false
	s.until = time.Time{}
	s.trialUntil = time.Time{}
	s.points = nil
	s.state = Open
}

func (s *hold) tick(now time.Time, log *eventLog, unit string) {
	if s == nil {
		return
	}
	if (!s.groupHold || s.until.IsZero() && s.trialUntil.IsZero()) && !s.at.IsZero() && now.Sub(s.at) >= holdMemory {
		s.step = 0
		s.at = time.Time{}
	}
	if s.until.After(now.Add(maxSavedHold)) {
		s.until = now.Add(maxSavedHold)
	}
	points := s.points[:0]
	for _, point := range s.points {
		if now.Sub(point.at) <= stallCorroborateWindow {
			points = append(points, point)
		}
	}
	clear(s.points[len(points):])
	s.points = points
	if !s.groupHold && !s.until.IsZero() && !now.Before(s.until) {
		expired := s.until
		s.until = time.Time{}
		if log != nil {
			log.addAt(expired, "%s", historyLine(historyTransition{prefix: "unblocked", unit: unit, reason: "cooldown ended"}))
		}
	}
	s.updateState()
}

func (s *hold) updateState() {
	switch {
	case !s.until.IsZero():
		s.state = Held
	case s.step > 0 && !s.stepOnly:
		s.state = Trial
	case len(s.points) > 0:
		s.state = Suspect
	default:
		s.state = Open
	}
}

func (s *hold) blocked() bool {
	return s != nil && s.state == Held
}
func (s *hold) onReply(log *eventLog, route string) {
	if !s.stepOnly && (!s.until.IsZero() || s.step > 0) {
		if log != nil {
			log.add("%s", historyLine(historyTransition{prefix: "unblocked", unit: route, reason: "first reply"}))
		}
	}
	s.clear()
}
func (s *hold) onStall(now time.Time, log *eventLog, route string, first bool, key string, pinned ...bool) bool {
	return s.onStallReason(now, log, route, "first reply", first, key, pinned...)
}

func (s *hold) onStallReason(now time.Time, log *eventLog, route, reason string, first bool, key string, pinned ...bool) bool {
	s.count++
	s.tick(now, log, route)
	if len(pinned) > 0 && pinned[0] {
		route += " (pinned)"
	}
	if !first {
		if log != nil {
			log.add("%s: connection closed, no reply (later)", route)
		}
		return false
	}
	duplicate := false
	for _, point := range s.points {
		duplicate = duplicate || point.key == key
	}
	if !duplicate {
		s.points = append(s.points, struct {
			key string
			at  time.Time
		}{key, now})
	}
	s.updateState()
	if duplicate || len(s.points) < 2 {
		if log != nil {
			log.add("%s: connection closed, no reply (first, single)", route)
		}
		return false
	}
	if len(pinned) > 0 && pinned[0] {
		if log != nil {
			log.add("%s: connection closed, no reply (first, pinned)", route)
		}
		return true
	}
	if s.blocked() {
		return true
	}
	s.enterHeld(now)
	if log != nil {
		log.stalled(now, s, route, reason, s.until.Sub(now), s.count)
	}
	return true
}
