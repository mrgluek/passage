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

func (s *subscriptionSchedule) first(jitter func(time.Duration) time.Duration) time.Duration {
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
	t.mu.Lock()
	mode := cmp.Or(t.only, t.mode)
	var dials []SubscriptionDial
	if d := t.ways[RouteTunnel].dial; d != nil && t.mode != ModeOff {
		dials = append(dials, d)
	}
	t.mu.Unlock()
	state := t.subs
	state.refreshMu.Lock()
	defer state.refreshMu.Unlock()
	state.mu.Lock()
	cfg := state.cfg
	cfg.Mode = mode
	state.mu.Unlock()
	if state.changed.Load() {
		return SubscriptionRefreshResult{Config: cfg, Changed: true}
	}
	state.refreshing.Store(true)
	defer state.refreshing.Store(false)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.ctx, cancel)
	defer func() { stop(); cancel() }()
	result := state.fetcher.Refresh(child, cfg, dials)
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
	for _, sub := range t.subs.cfg.Subscriptions {
		if tried := time.Unix(sub.LastTry, 0); sub.LastTry > 0 && tried.After(schedule.lastUrgent) {
			schedule.lastUrgent = tried
		}
	}
	t.subs.mu.Unlock()
	timer := time.NewTimer(schedule.first(subscriptionJitter))
	defer timer.Stop()
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
		result := t.RefreshSubscriptions(t.ctx)
		schedule.lastUrgent = time.Now()
		ok := true
		for _, sub := range result.Config.Subscriptions {
			if sub.LastError != "" && sub.LastError != "unchanged" && sub.LastError != "collision" {
				ok = false
			}
		}
		timer.Reset(schedule.next(ok, subscriptionJitter))
	}
}
