package tunnel

import (
	"cmp"
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

type subscriptionRuntime struct {
	mu         sync.Mutex
	refreshMu  sync.Mutex
	refreshed  bool
	cfg        Config
	fetcher    SubscriptionRefresher
	updates    chan SubscriptionRefreshResult
	urgent     chan struct{}
	changed    atomic.Bool
	refreshing atomic.Bool
}

// subscriptionSchedule uses caller-supplied times and jitter for deterministic tests.
type subscriptionSchedule struct {
	failures   int
	lastUrgent time.Time
}

func (s *subscriptionSchedule) first(cfg Config, jitter func(time.Duration) time.Duration) time.Duration {
	installed := make(map[string]bool, len(cfg.Routes))
	for _, route := range cfg.Routes {
		installed[route.ID] = true
	}
	for _, sub := range cfg.Subscriptions {
		if sub.Closed {
			continue
		}
		hasRoutes := false
		for _, id := range sub.RouteIDs {
			hasRoutes = hasRoutes || installed[id]
		}
		if sub.LastTry == 0 || !hasRoutes {
			return 0
		}
	}
	return 5*time.Second + jitter(55*time.Second)
}
func (s *subscriptionSchedule) next(ok bool, jitter func(time.Duration) time.Duration) time.Duration {
	if ok {
		s.failures = 0
	} else {
		s.failures++
	}
	switch s.failures {
	case 1:
		return 5 * time.Minute
	case 2:
		return 15 * time.Minute
	case 3:
		return time.Hour
	default:
		return 6*time.Hour + jitter(6*time.Hour)
	}
}
func (s *subscriptionSchedule) urgent(now time.Time) bool {
	if !s.lastUrgent.IsZero() && now.Sub(s.lastUrgent) < 15*time.Minute {
		return false
	}
	s.lastUrgent = now
	return true
}
func subscriptionJitter(limit time.Duration) time.Duration {
	return time.Duration(rand.Int64N(int64(limit) + 1))
}

// SubscriptionUpdates carries private configs for persistence and restart outside Status.
func (t *Tunnel) SubscriptionUpdates() <-chan SubscriptionRefreshResult { return t.subs.updates }

func (t *Tunnel) requestSubscriptionRefresh() {
	if t.subs == nil || t.subs.refreshing.Load() {
		return
	}
	select {
	case t.subs.urgent <- struct{}{}:
	default:
	}
}

// RefreshSubscriptions returns the latest private config. Changed stays true until restart.
func (t *Tunnel) RefreshSubscriptions(ctx context.Context) SubscriptionRefreshResult {
	return t.refreshSubscriptions(ctx, false)
}

// RefreshInitialSubscriptions shares the first refresh with router startup.
func (t *Tunnel) RefreshInitialSubscriptions(ctx context.Context) SubscriptionRefreshResult {
	return t.refreshSubscriptions(ctx, true)
}

func (t *Tunnel) refreshSubscriptions(ctx context.Context, initial bool) SubscriptionRefreshResult {
	t.mu.Lock()
	mode := cmp.Or(t.only, t.mode)
	var dials []SubscriptionDial
	if d := t.ways[RouteTunnel].dial; d != nil && t.mode != ModeOff {
		dials = append(dials, d)
	}
	t.mu.Unlock()
	state := t.subs
	joined := !state.refreshMu.TryLock()
	if joined {
		state.refreshMu.Lock()
	}
	defer state.refreshMu.Unlock()
	state.mu.Lock()
	cfg := state.cfg
	cfg.Mode = mode
	state.mu.Unlock()
	if state.changed.Load() || (state.refreshed && (initial || joined)) {
		return SubscriptionRefreshResult{Config: cfg, Changed: state.changed.Load()}
	}
	state.refreshing.Store(true)
	defer state.refreshing.Store(false)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.ctx, cancel)
	defer func() { stop(); cancel() }()
	result := state.fetcher.Refresh(child, cfg, dials)
	state.refreshed = true
	state.mu.Lock()
	state.cfg = result.Config
	state.mu.Unlock()
	if result.Changed {
		state.changed.Store(true)
	}
	result.Changed = state.changed.Load()
	// Coalesce metadata notifications while preserving a pending route change.
	select {
	case <-state.updates:
	default:
	}
	state.updates <- result
	return result
}

func (t *Tunnel) subscriptionLoop() {
	defer t.wg.Done()
	var schedule subscriptionSchedule
	t.subs.mu.Lock()
	active := false
	for _, sub := range t.subs.cfg.Subscriptions {
		if sub.Closed {
			continue
		}
		active = true
		if tried := time.Unix(sub.LastTry, 0); sub.LastTry > 0 && tried.After(schedule.lastUrgent) {
			schedule.lastUrgent = tried
		}
	}
	cfg := t.subs.cfg
	t.subs.mu.Unlock()
	if !active {
		return
	}
	timer := time.NewTimer(schedule.first(cfg, subscriptionJitter))
	defer timer.Stop()
	first := true
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-timer.C:
		case <-t.subs.urgent:
			if !schedule.urgent(time.Now()) {
				continue
			}
		}
		if t.ctx.Err() != nil {
			return
		}
		result := t.refreshSubscriptions(t.ctx, first)
		first = false
		schedule.lastUrgent = time.Now()
		ok := true
		active = false
		for _, sub := range result.Config.Subscriptions {
			if sub.Closed {
				continue
			}
			active = true
			if sub.LastError != "" && sub.LastError != "unchanged" && sub.LastError != "collision" {
				ok = false
			}
		}
		if !active {
			return
		}
		timer.Reset(schedule.next(ok, subscriptionJitter))
	}
}
