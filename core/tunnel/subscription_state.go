package tunnel

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"

	links "github.com/mrgluek/passage-link/link"
)

// SaveSubscriptionState writes private subscription routes and metadata atomically.
func SaveSubscriptionState(path string, cfg Config) error {
	state := Config{Subscriptions: cfg.Subscriptions, UpdateSources: cfg.UpdateSources}
	owned := map[string]bool{}
	for _, sub := range cfg.Subscriptions {
		for _, id := range sub.RouteIDs {
			owned[id] = true
		}
	}
	for _, route := range cfg.Routes {
		if owned[route.ID] {
			state.Routes = append(state.Routes, route)
		}
	}
	state.RouteEdits = MinimalEdits(state.Routes, cfg.RouteEdits)
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".subscriptions-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// RestoreSubscriptionState restores only subscriptions still authorized by an invite.
func RestoreSubscriptionState(cfg Config, path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var stored Config
	if err = json.Unmarshal(raw, &stored); err != nil {
		return cfg, err
	}
	if _, err = stored.Normalize(); err != nil {
		return cfg, err
	}
	cfg.Subscriptions = slices.Clone(cfg.Subscriptions)
	cfg.RouteEdits = maps.Clone(cfg.RouteEdits)
	cfg.Routes = slices.Clone(cfg.Routes)
	for j, sub := range cfg.Subscriptions {
		for _, saved := range stored.Subscriptions {
			if saved.ID != sub.ID || !bytes.Equal(saved.Signer, sub.Signer) || saved.Revision < sub.Revision {
				continue
			}
			if len(cfg.Routes) == 0 && cfg.Zone != "" {
				cfg.Routes = legacyRoutes(cfg)
				cfg.Zone, cfg.Key = "", ""
			}
			own := map[string]bool{}
			for _, id := range sub.RouteIDs {
				own[id] = true
			}
			// A subscription signed by the trusted author takes over manual routes of
			// the same id, as a refresh does. A route owned by another subscription
			// is never taken over.
			keys, _ := authorKeys()
			trusted := len(keys) > 0 && bytes.Equal(saved.Signer, keys[0])
			otherOwned := map[string]bool{}
			for k, other := range cfg.Subscriptions {
				if k != j {
					for _, id := range other.RouteIDs {
						otherOwned[id] = true
					}
				}
			}
			if saved.LastError == "collision" {
				saved.LastError = ""
			}
			routes := make([]links.Route, 0, len(cfg.Routes))
			blocked := map[string]bool{}
			for _, r := range cfg.Routes {
				if !own[r.ID] {
					routes = append(routes, r)
					blocked[r.ID] = true
				}
			}
			savedIDs := slices.Clone(saved.RouteIDs)
			saved.RouteIDs = nil
			for _, r := range stored.Routes {
				if !slices.Contains(savedIDs, r.ID) {
					continue
				}
				if blocked[r.ID] {
					if !trusted || otherOwned[r.ID] {
						saved.LastError = "collision"
						continue
					}
					routes = slices.DeleteFunc(routes, func(x links.Route) bool { return x.ID == r.ID })
				}
				routes = append(routes, r)
				saved.RouteIDs = append(saved.RouteIDs, r.ID)
			}
			if !slices.Equal(saved.Sources, sub.Sources) || !bytes.Equal(saved.ReadKey, sub.ReadKey) {
				saved.ETag, saved.ETagSource = "", ""
			}
			saved.Name, saved.Sources, saved.ReadKey = sub.Name, slices.Clone(sub.Sources), slices.Clone(sub.ReadKey)
			cfg.Subscriptions[j] = saved
			cfg.Routes = links.Sorted(routes)
			if cfg.RouteEdits == nil {
				cfg.RouteEdits = map[string]RouteEdit{}
			}
			for _, id := range saved.RouteIDs {
				if edit, ok := stored.RouteEdits[id]; ok {
					cfg.RouteEdits[id] = edit
				}
			}
			if keys, e := authorKeys(); e == nil && len(keys) > 0 && bytes.Equal(saved.Signer, keys[0]) && len(stored.UpdateSources) > 0 {
				cfg.UpdateSources = slices.Clone(stored.UpdateSources)
			}
			break
		}
	}
	cfg.RouteEdits = MinimalEdits(cfg.Routes, cfg.RouteEdits)
	return cfg.Normalize()
}
