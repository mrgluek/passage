package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

// The private sidecar is authoritative once it exists, including an empty list.
func (a *App) restoreSubscriptions() error {
	raw, err := os.ReadFile(a.path("subscriptions.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state tunnel.Config
	if err = json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if _, err = state.Normalize(); err != nil {
		return err
	}
	cfg := a.linkCfg
	for _, sub := range cfg.Subscriptions {
		cfg, _ = tunnel.RemoveSubscription(cfg, sub.ID)
	}
	cfg.Subscriptions = slices.Clone(state.Subscriptions)
	for i := range cfg.Subscriptions {
		cfg.Subscriptions[i].RouteIDs = nil
	}
	cfg, err = tunnel.RestoreSubscriptionState(cfg, a.path("subscriptions.json"))
	if err == nil {
		a.linkCfg = cfg
	}
	return err
}

type invitePreview struct {
	Mode      string `json:"mode"`
	Text      string `json:"text"`
	Collision bool   `json:"collision"`
}

func (a *App) previewImport(text string) (invitePreview, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := tunnel.ImportLink(a.linkCfg, strings.TrimSpace(text), false)
	if err == nil && r.Mode == "subscribe" {
		d := r.Subscription
		signer := a.t.f("sub_unknown")
		if r.Signature == "trusted_author" {
			signer = a.t.f("sub_trusted")
		}
		seed := a.t.f("sub_no_seed")
		if d.HasSeed {
			seed = a.t.f("sub_seed")
		}
		body := d.Name + "\n" + a.t.f("sub_addresses", d.Sources) + "\n" + seed + "\n\n" + signer
		if r.Collision {
			body += "\n\n" + a.t.f("sub_collision")
		}
		return invitePreview{"subscribe", body, r.Collision}, nil
	}
	if err := a.checkRouteLinks(text); err != nil {
		return invitePreview{}, err
	}
	body, err := a.importPreview(text)
	return invitePreview{Mode: "routes", Text: body}, err
}

func (a *App) subscribe(text string, replace bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.changedAfter()
	r, err := tunnel.SubscribeLink(a.linkCfg, strings.TrimSpace(text), replace)
	if err != nil {
		return err
	}
	if r.Collision && !replace {
		return errors.New(a.t.f("sub_collision"))
	}
	return a.applySubscriptionsLocked(r.Config, true)
}

func (a *App) applySubscriptionsLocked(cfg tunnel.Config, restart bool) error {
	if err := tunnel.SaveSubscriptionState(a.path("subscriptions.json"), cfg); err != nil {
		return errors.New(a.t.f("sub_save_failed"))
	}
	a.linkCfg = cfg
	a.subscriptionEpoch++
	a.settings.Edits = tunnel.MinimalEdits(cfg.Routes, cfg.RouteEdits)
	if !a.available(a.settings.Mode) {
		a.settings.Mode = tunnel.ModeAuto
	}
	a.saveSettings()
	if restart {
		a.restartIfRunningLocked("subscription changed")
	}
	return nil
}

func (a *App) removeSubscription(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.changedAfter()
	cfg, err := tunnel.RemoveSubscription(a.linkCfg, id)
	if err != nil {
		return err
	}
	return a.applySubscriptionsLocked(cfg, true)
}

func subscriptionFailure(t tr, code string) string {
	switch code {
	case "", "unchanged":
		return ""
	case "network", "http", "size", "decrypt", "signature", "rollback", "collision", "required":
		return t.f("sub_error_" + code)
	default:
		return t.f("sub_error_network")
	}
}

func refreshResult(t tr, before, after tunnel.Subscription) string {
	if msg := subscriptionFailure(t, after.LastError); msg != "" {
		return t.f("sub_failed", msg)
	}
	if after.Revision > before.Revision {
		return t.f("sub_updated")
	}
	return t.f("sub_current")
}

type subscriptionRow struct {
	Closed bool   `json:"closed"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Result string `json:"result"`
	Busy   bool   `json:"busy"`
}

func (a *App) subscriptionRowsLocked() []subscriptionRow {
	rows := []subscriptionRow{}
	for _, sub := range a.linkCfg.Subscriptions {
		detail := a.t.f("sub_detail", sub.Revision, len(sub.RouteIDs))
		status := subscriptionFailure(a.t, sub.LastError)
		if status == "" && sub.LastOK > 0 {
			mins := max(int64(0), (time.Now().Unix()-sub.LastOK)/60)
			switch {
			case mins < 1:
				status = a.t.f("sub_just_now")
			case mins < 60:
				status = a.t.f("sub_minutes", mins)
			case mins < 1440:
				status = a.t.f("sub_hours", mins/60)
			default:
				status = a.t.f("sub_days", mins/1440)
			}
		}
		if status != "" {
			detail += " · " + status
		}
		result, busy := a.subscriptionResults[sub.ID], a.subscriptionRefreshing
		if sub.Closed {
			detail, result, busy = a.t.f("sub_closed"), "", false
		}
		rows = append(rows, subscriptionRow{sub.Closed, sub.ID, sub.Name, detail, result, busy})
	}
	return rows
}

func routeChanges(before, after tunnel.Config) (added, removed int) {
	old, next := map[string]bool{}, map[string]bool{}
	for _, r := range before.Routes {
		old[r.ID] = true
	}
	for _, r := range after.Routes {
		next[r.ID] = true
		if !old[r.ID] {
			added++
		}
	}
	for id := range old {
		if !next[id] {
			removed++
		}
	}
	return
}

func (a *App) refreshSubscriptions(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.subscriptionRefreshing {
		return nil
	}
	if !slices.ContainsFunc(a.linkCfg.Subscriptions, func(s tunnel.Subscription) bool { return s.ID == id }) {
		return errors.New("unknown subscription")
	}
	a.subscriptionRefreshing = true
	a.subscriptionResults = map[string]string{}
	cfg, tun, epoch := a.linkCfg, a.tun, a.subscriptionEpoch
	go func() {
		var out tunnel.SubscriptionRefreshResult
		if tun != nil {
			out = tun.RefreshSubscriptions(context.Background())
		} else {
			var ref tunnel.SubscriptionRefresher
			out = ref.Refresh(context.Background(), cfg, nil)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		defer a.changedAfter()
		a.subscriptionRefreshing = false
		if a.subscriptionEpoch != epoch || a.tun != tun {
			return
		}
		if tun != nil {
			select {
			case <-tun.SubscriptionUpdates():
			default:
			}
		}
		if err := a.applySubscriptionsLocked(out.Config, out.Changed); err != nil {
			a.subscriptionResults[id] = err.Error()
			return
		}
		for _, after := range out.Config.Subscriptions {
			for _, before := range cfg.Subscriptions {
				if before.ID == after.ID {
					a.subscriptionResults[after.ID] = refreshResult(a.t, before, after)
				}
			}
		}
	}()
	return nil
}

func (a *App) checkRouteLinks(text string) error {
	for _, line := range strings.Split(text, "\n") {
		r, err := tunnel.ImportLink(a.linkCfg, strings.TrimSpace(line), false)
		if err == nil && r.Mode == "subscribe" {
			return errors.New(a.t.f("sub_one"))
		}
	}
	return nil
}
