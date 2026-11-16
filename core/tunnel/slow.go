package tunnel

import (
	"context"
	"net"
	"time"
)

// A direct connection that is slow to open is a sign of a poor path: packets
// are being lost and the handshake keeps being retransmitted. The probes only
// ask whether the path connects at all, so a path like that stays "direct"
// while every message waits. These pieces measure it on real connections:
//   - a hedge: when the direct connect has not finished after hedgeDelay, the
//     tunnel is started alongside it and the first one to connect is used;
//   - a slow period: when direct keeps losing that race, the group uses the
//     tunnel first for a while and direct only as the hedge.
const (
	// This many slow connects within slowWindow start a slow period.
	slowEventsToPrefer = 3
	slowWindow         = 5 * time.Minute
	// Fast direct connects in a row that end the memory of earlier slow periods.
	fastToForget = 5
	// A slow period that follows another within this time lasts longer.
	slowMemory = time.Hour
)

// Variables allow short times in tests.
var (
	// hedgeDelay is a little over the first retransmission of a lost SYN (one
	// second on the common systems), so a clean path is not doubled.
	hedgeDelay = 1500 * time.Millisecond
	// A direct connect that wins the race but took this long still counts as slow.
	// Mobile and congested paths often need a few seconds, so this is generous.
	slowConnect = 5 * time.Second
	// While the tunnel is preferred, direct is started only if it takes this long.
	preferDelay = 5 * time.Second
)

var slowDelays = [...]time.Duration{3 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// slowTrack is one group's record of slow direct connects.
type slowTrack struct {
	events []time.Time
	until  time.Time
	step   int
	last   time.Time
	fast   int
}

// active reports whether the tunnel is preferred for the group at now.
func (s *slowTrack) active(now time.Time) bool { return s != nil && now.Before(s.until) }

// slow records a slow direct connect. It returns the length of the slow period
// when this one starts it, or 0.
func (s *slowTrack) slow(now time.Time) time.Duration {
	s.fast = 0
	if s.active(now) {
		return 0
	}
	kept := s.events[:0]
	for _, at := range s.events {
		if now.Sub(at) < slowWindow {
			kept = append(kept, at)
		}
	}
	s.events = append(kept, now)
	if len(s.events) < slowEventsToPrefer {
		return 0
	}
	if now.Sub(s.last) >= slowMemory {
		s.step = 0
	}
	s.step++
	delay := slowDelays[min(s.step, len(slowDelays))-1]
	s.until, s.last, s.events = now.Add(delay), now, nil
	return delay
}

// fastDial records a direct connect that finished before the hedge started. It
// reports whether enough of them in a row ended the memory of slow periods.
func (s *slowTrack) fastDial(now time.Time) bool {
	if s.active(now) {
		return false
	}
	s.events = nil
	s.fast++
	if s.fast >= fastToForget && s.step > 0 {
		s.step, s.fast = 0, 0
		return true
	}
	return false
}

// raceOutcome says how a race ended.
type raceOutcome struct {
	conn    net.Conn
	winner  int // 0 the primary, 1 the secondary
	hedged  bool
	took    time.Duration
	err     error
	primary error
}

// raceDial dials with primary and, if it has not finished after delay, also
// with secondary; the first to connect wins and the other is cancelled, or
// closed if it connects late. When the primary fails before the delay the
// secondary is never started and the primary's error is returned, so the
// caller's own fallback applies.
func raceDial(ctx context.Context, primary, secondary dialFunc, pTimeout, sTimeout, delay time.Duration, addr string) raceOutcome {
	type result struct {
		conn net.Conn
		err  error
		i    int
		took time.Duration
	}
	start := time.Now()
	results := make(chan result, 2)
	pctx, pcancel := context.WithTimeout(ctx, pTimeout)
	go func() {
		c, err := primary(pctx, "tcp", addr)
		results <- result{c, err, 0, time.Since(start)}
	}()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	cancels := [2]context.CancelFunc{pcancel, func() {}}
	pending, hedged := 1, false
	var out raceOutcome
	for pending > 0 {
		select {
		case <-timer.C:
			if !hedged {
				hedged = true
				pending++
				sctx, scancel := context.WithTimeout(ctx, sTimeout)
				cancels[1] = scancel
				go func() {
					c, err := secondary(sctx, "tcp", addr)
					results <- result{c, err, 1, time.Since(start)}
				}()
			}
		case r := <-results:
			pending--
			if r.err == nil {
				cancels[1-r.i]()
				if pending > 0 {
					go func() {
						if late := <-results; late.err == nil {
							late.conn.Close()
						}
						cancels[r.i]()
					}()
				} else {
					cancels[r.i]()
				}
				return raceOutcome{conn: r.conn, winner: r.i, hedged: hedged, took: r.took, primary: out.primary}
			}
			if r.i == 0 {
				out.primary = r.err
				if !hedged {
					cancels[0]()
					return raceOutcome{hedged: false, took: r.took, err: r.err, primary: r.err}
				}
			}
			if out.err == nil || r.i == 0 {
				out.err = r.err
			}
		}
	}
	cancels[0]()
	cancels[1]()
	out.hedged = hedged
	return out
}
