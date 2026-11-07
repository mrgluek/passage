package tunnel

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	links "github.com/mrgluek/passage-link/link"
)

// Only package tests enable HTTP fixtures.
var subscriptionTestHTTP bool

// Subscription holds private subscription state; only SubscriptionStatus is public.
type Subscription struct {
	Closed     bool     `json:"closed,omitempty"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Signer     []byte   `json:"signer"`
	Sources    []string `json:"sources"`
	ReadKey    []byte   `json:"read_key"`
	Revision   uint64   `json:"revision"`
	ETag       string   `json:"etag,omitempty"`
	ETagSource string   `json:"etag_source,omitempty"`
	RouteIDs   []string `json:"route_ids,omitempty"`
	LastOK     int64    `json:"last_ok"`
	LastTry    int64    `json:"last_try"`
	LastError  string   `json:"last_error,omitempty"`
}

type SubscriptionStatus struct {
	Closed    bool   `json:"closed,omitempty"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Revision  uint64 `json:"revision"`
	Routes    int    `json:"routes"`
	LastOK    int64  `json:"last_ok"`
	LastTry   int64  `json:"last_try"`
	LastError string `json:"last_error,omitempty"`
}

type SubscriptionDescription struct {
	Name    string `json:"name"`
	Sources int    `json:"sources"`
	HasSeed bool   `json:"has_seed"`
}

func authorKeys() ([]ed25519.PublicKey, error) {
	if AuthorKey == "" {
		return nil, nil
	}
	pub, err := base64.RawURLEncoding.DecodeString(AuthorKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("invalid build author key")
	}
	return []ed25519.PublicKey{pub}, nil
}

func (s Subscription) validate() error {
	switch s.LastError {
	case "", "network", "http", "size", "decrypt", "signature", "rollback", "unchanged", "required", "collision":
	default:
		return errors.New("invalid subscription error code")
	}
	id, err := hex.DecodeString(s.ID)
	if err != nil || hex.EncodeToString(id) != s.ID {
		return errors.New("invalid subscription id")
	}
	sources := slices.Clone(s.Sources)
	if subscriptionTestHTTP {
		for j, source := range sources {
			sources[j] = strings.Replace(source, "http://", "https://", 1)
		}
	}
	return (links.Invite{V: 2, Kind: "sub", ID: id, Name: s.Name, Signer: s.Signer, Sources: sources, ReadKey: s.ReadKey, Required: []string{"sub"}}).Validate()
}

// ApplySubFile applies an already opened bundle, preserving other owners and local edits.
func ApplySubFile(cfg Config, sub Subscription, bundle links.Bundle) (Config, error) {
	id, err := hex.DecodeString(sub.ID)
	if err != nil || !bytes.Equal(bundle.Sub, id) || !bytes.Equal(bundle.Signer, sub.Signer) {
		return cfg, errors.New("signature")
	}
	if len(bundle.Required) != 0 {
		return cfg, errors.New("required: update Passage")
	}
	index := slices.IndexFunc(cfg.Subscriptions, func(s Subscription) bool { return s.ID == sub.ID })
	if index < 0 || !bytes.Equal(cfg.Subscriptions[index].Signer, sub.Signer) {
		return cfg, errors.New("signature")
	}
	if err := links.CheckRevision(cfg.Subscriptions[index].Revision, bundle.Revision); err != nil {
		return cfg, err
	}
	if cfg.Subscriptions[index].Closed {
		return cfg, errors.New("closed")
	}
	if bundle.Mode != "replace" {
		return cfg, errors.New("signature")
	}
	if _, err := bundle.Validate(); err != nil {
		return cfg, err
	}
	if bundle.Closed {
		next, err := RemoveSubscription(cfg, sub.ID)
		if err != nil {
			return cfg, err
		}
		lastTry := sub.LastTry
		sub = cfg.Subscriptions[index]
		sub.LastTry = lastTry
		sub.Closed, sub.Revision, sub.RouteIDs = true, bundle.Revision, nil
		sub.LastError, sub.LastOK = "", time.Now().Unix()
		next.Subscriptions = slices.Insert(next.Subscriptions, index, sub)
		return next.Normalize()
	}
	trusted, err := authorKeys()
	if err != nil {
		return cfg, err
	}
	trustedAuthor := len(trusted) > 0 && bytes.Equal(sub.Signer, trusted[0])
	sub.RouteIDs = slices.Clone(cfg.Subscriptions[index].RouteIDs)
	if len(cfg.Routes) == 0 && cfg.Zone != "" {
		cfg.Routes = legacyRoutes(cfg)
	}
	cfg.Zone, cfg.Key = "", ""

	// Work on copies so a failed transaction cannot change the caller's config.
	cfg.Subscriptions = slices.Clone(cfg.Subscriptions)
	owned := map[string]bool{}
	for _, rid := range sub.RouteIDs {
		owned[rid] = true
	}
	otherOwned := map[string]bool{}
	for _, other := range cfg.Subscriptions {
		if other.ID != sub.ID {
			for _, rid := range other.RouteIDs {
				otherOwned[rid] = true
				delete(owned, rid)
			}
		}
	}
	takeover := map[string]bool{}
	if trustedAuthor {
		for _, r := range bundle.Routes {
			supported, _ := links.ValidateRoute(r)
			if supported && !otherOwned[r.ID] {
				takeover[r.ID] = true
			}
		}
	}
	blocked := map[string]bool{}
	var routes []links.Route
	for _, r := range cfg.Routes {
		if !owned[r.ID] && !takeover[r.ID] {
			routes = append(routes, r)
			blocked[r.ID] = true
		}
	}
	sub.RouteIDs = nil
	sub.LastError = ""
	for _, r := range bundle.Routes {
		supported, _ := links.ValidateRoute(r)
		if !supported {
			continue
		}
		if blocked[r.ID] {
			sub.LastError = "collision"
			continue
		}
		routes = append(routes, r)
		sub.RouteIDs = append(sub.RouteIDs, r.ID)
	}
	sub.Revision = bundle.Revision
	// The signed file carries the current name, so the owner can rename a
	// subscription after it was installed.
	if bundle.Name != "" && utf8.ValidString(bundle.Name) {
		sub.Name = bundle.Name
	}
	cfg.Routes = links.Sorted(routes)
	cfg.RouteEdits = MinimalEdits(cfg.Routes, cfg.RouteEdits)
	cfg.Subscriptions[index] = sub
	if trustedAuthor && len(bundle.Updates) > 0 {
		cfg.UpdateSources = slices.Clone(bundle.Updates)
	}
	return cfg.Normalize()
}

// SubscribeLink is the explicit activation step after an invite preview.
func SubscribeLink(cfg Config, text string, replaceSigner bool) (ImportResult, error) {
	trusted, err := authorKeys()
	if err != nil {
		return ImportResult{}, err
	}
	decoded, err := links.DecodeInvite([]byte(text), trusted)
	if err != nil {
		return ImportResult{}, err
	}
	i := decoded.Invite
	out := ImportResult{Config: cfg, Signature: decoded.Signature, Mode: "subscribe", Subscription: &SubscriptionDescription{i.Name, len(i.Sources), i.Seed != nil}}
	sub := Subscription{ID: hex.EncodeToString(i.ID), Name: i.Name, Signer: slices.Clone(i.Signer), Sources: slices.Clone(i.Sources), ReadKey: slices.Clone(i.ReadKey)}
	at := slices.IndexFunc(cfg.Subscriptions, func(s Subscription) bool { return s.ID == sub.ID })
	cfg.Subscriptions = slices.Clone(cfg.Subscriptions)
	if at >= 0 {
		old := cfg.Subscriptions[at]
		if old.Closed {
			return out, errors.New("closed")
		}
		if !bytes.Equal(old.Signer, sub.Signer) {
			out.Collision = true
			if !replaceSigner {
				return out, nil
			}
			// A confirmed signer change retains ownership, but resets revision trust.
			sub.RouteIDs = slices.Clone(old.RouteIDs)
		} else {
			sub.Revision, sub.RouteIDs = old.Revision, slices.Clone(old.RouteIDs)
			sub.LastOK, sub.LastTry, sub.LastError = old.LastOK, old.LastTry, old.LastError
		}
		cfg.Subscriptions[at] = sub
	} else {
		cfg.Subscriptions = append(cfg.Subscriptions, sub)
	}
	if i.Seed != nil {
		opened, e := links.OpenSubFile(i.Seed, i.ID, i.ReadKey, ed25519.PublicKey(i.Signer))
		if e != nil {
			return out, e
		}
		cfg, err = ApplySubFile(cfg, sub, opened.Bundle)
		if errors.Is(err, links.ErrNotNewer) {
			err = nil
		}
		if err != nil {
			return out, err
		}
	}
	out.Config, err = cfg.Normalize()
	if err == nil {
		for _, s := range out.Config.Subscriptions {
			if s.ID == sub.ID {
				out.IDs = slices.Clone(s.RouteIDs)
				out.Count = len(s.RouteIDs)
			}
		}
	}
	return out, err
}

func subscriptionStatuses(cfg Config) []SubscriptionStatus {
	out := make([]SubscriptionStatus, 0, len(cfg.Subscriptions))
	for _, s := range cfg.Subscriptions {
		out = append(out, SubscriptionStatus{s.Closed, s.ID[:min(8, len(s.ID))], s.Name, s.Revision, len(s.RouteIDs), s.LastOK, s.LastTry, s.LastError})
	}
	return out
}

func subscriptionError(err error) string {
	switch {
	case errors.Is(err, links.ErrRollback):
		return "rollback"
	case errors.Is(err, links.ErrNotNewer):
		return "unchanged"
	case strings.Contains(err.Error(), "required"), strings.Contains(err.Error(), "update Passage"):
		return "required"
	case strings.Contains(err.Error(), "signature"):
		return "signature"
	default:
		return "decrypt"
	}
}

// RemoveSubscription removes the subscription and its remaining owned routes.
func RemoveSubscription(cfg Config, id string) (Config, error) {
	at := slices.IndexFunc(cfg.Subscriptions, func(s Subscription) bool { return s.ID == id })
	if at < 0 {
		return cfg, errors.New("unknown subscription")
	}
	owned := map[string]bool{}
	for _, rid := range cfg.Subscriptions[at].RouteIDs {
		owned[rid] = true
	}
	for _, sub := range cfg.Subscriptions {
		if sub.ID != id {
			for _, rid := range sub.RouteIDs {
				delete(owned, rid)
			}
		}
	}
	cfg.Subscriptions = slices.Delete(slices.Clone(cfg.Subscriptions), at, at+1)
	cfg.Routes = slices.DeleteFunc(slices.Clone(cfg.Routes), func(r links.Route) bool { return owned[r.ID] })
	cfg.RouteEdits = maps.Clone(cfg.RouteEdits)
	for rid := range owned {
		delete(cfg.RouteEdits, rid)
	}
	return cfg, nil
}
