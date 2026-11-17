package tunnel

import (
	"slices"
	"time"
)

const routeEventLimit = 50

type routeEventKind uint8

const (
	routeWon routeEventKind = iota
	routeLost
	routeLater
	routeStopped
)

type routeEvent struct {
	connect time.Duration
	kind    routeEventKind
}

type routeEventWindow struct {
	events [routeEventLimit]routeEvent
	next   int
	size   int
}

// RouteRecentStats counts recent foreground events for one tunnel route.
type RouteRecentStats struct {
	Won       int             `json:"won"`
	Lost      int             `json:"lost"`
	ConnectMS *RouteConnectMS `json:"connect_ms,omitempty"`
	Later     int             `json:"later"`   // closed waiting for a later reply
	Stopped   int             `json:"stopped"` // closed waiting for the first reply
}

// RouteConnectMS gives the median and nearest-rank p90 of winning dial times.
type RouteConnectMS struct {
	Median int64 `json:"median"`
	P90    int64 `json:"p90"`
}

// recordRace counts only attempts launched in a race that found a winner.
func (p *routePool) recordRace(started []int, winner int, connect time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, i := range started {
		event := routeEvent{kind: routeLost}
		if i == winner {
			event = routeEvent{kind: routeWon, connect: connect}
		}
		p.recordRouteEventLocked(i, event)
	}
}

func (p *routePool) recordRouteEventLocked(i int, event routeEvent) {
	if i >= len(p.clients) || i >= len(p.ids) || p.ids[i] == "" {
		return
	}
	if p.recent == nil {
		p.recent = make([]routeEventWindow, len(p.clients))
	}
	w := &p.recent[i]
	w.events[w.next] = event
	w.next = (w.next + 1) % routeEventLimit
	w.size = min(w.size+1, routeEventLimit)
}

func (w *routeEventWindow) snapshot() *RouteRecentStats {
	if w.size == 0 {
		return nil
	}
	var stats RouteRecentStats
	var connects [routeEventLimit]time.Duration
	for _, event := range w.events[:w.size] {
		switch event.kind {
		case routeWon:
			connects[stats.Won] = event.connect
			stats.Won++
		case routeLost:
			stats.Lost++
		case routeLater:
			stats.Later++
		case routeStopped:
			stats.Stopped++
		}
	}
	n := stats.Won
	if n > 0 {
		slices.Sort(connects[:n])
		lower, upper := connects[(n-1)/2], connects[n/2]
		stats.ConnectMS = &RouteConnectMS{
			Median: (lower + (upper-lower)/2).Milliseconds(),
			P90:    connects[(9*n-1)/10].Milliseconds(),
		}
	}
	return &stats
}
