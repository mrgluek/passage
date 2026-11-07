package tunnel

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	links "github.com/mrgluek/passage-link/link"
)

// AuthorKey is supplied by the local release build, never by a link.
var AuthorKey string

type ImportResult struct {
	Config       Config                   `json:"config"`
	Signature    string                   `json:"signature"`
	Mode         string                   `json:"mode"`
	Count        int                      `json:"count"`
	IDs          []string                 `json:"route_ids"`
	Collision    bool                     `json:"collision"`
	Subscription *SubscriptionDescription `json:"subscription,omitempty"`
}

func ImportLink(existing Config, text string, replaceIDs bool) (ImportResult, error) {
	var result ImportResult
	if strings.TrimSpace(text) == "" {
		result.Mode = "replace"
		result.IDs = []string{}
		return result, nil
	}
	if !strings.Contains(text, "#v2.") {
		cfg, err := ParseLink(text)
		if err != nil {
			return result, err
		}
		result.Config = cfg
		result.Signature = "unsigned_legacy"
		result.Mode = "replace"
		result.Count = 1
		result.IDs = []string{"legacy-dns"}
		return result, nil
	}
	trusted, err := authorKeys()
	if err != nil {
		return result, err
	}
	if invite, e := links.DecodeInvite([]byte(text), trusted); e == nil {
		result.Config, result.Signature, result.Mode = existing, invite.Signature, "subscribe"
		result.Subscription = &SubscriptionDescription{invite.Invite.Name, len(invite.Invite.Sources), invite.Invite.Seed != nil}
		for _, sub := range existing.Subscriptions {
			if sub.ID == hex.EncodeToString(invite.Invite.ID) && !bytes.Equal(sub.Signer, invite.Invite.Signer) {
				result.Collision = true
			}
		}
		return result, nil
	}
	decoded, err := links.ParseURI(text, trusted)
	if err != nil {
		return result, err
	}
	if decoded.Bundle.Sub != nil {
		return result, errors.New("subscription bundle requires an invite")
	}
	for _, r := range decoded.Bundle.Routes {
		result.IDs = append(result.IDs, r.ID)
	}
	routes := existing.Routes
	if len(routes) == 0 && existing.Zone != "" {
		routes = legacyRoutes(existing)
	}
	installed, err := links.Apply(routes, decoded.Bundle, false)
	if err != nil {
		if _, check := links.Apply(routes, decoded.Bundle, true); check != nil {
			return result, err
		}
		result.Collision = true
		if replaceIDs {
			installed, err = links.Apply(routes, decoded.Bundle, true)
		} else {
			installed = nil
			err = nil
		}
	}
	if err != nil {
		return result, err
	}
	result.Signature = decoded.Signature
	result.Mode = decoded.Bundle.Mode
	result.Count = len(decoded.Bundle.Routes)
	result.Config = existing
	result.Config.Zone = ""
	result.Config.Key = ""
	result.Config.Routes = installed
	result.Config.Subscriptions = slices.Clone(existing.Subscriptions)
	for j, sub := range result.Config.Subscriptions {
		var kept []string
		for _, id := range sub.RouteIDs {
			if decoded.Bundle.Mode != "replace" && !slices.Contains(result.IDs, id) {
				kept = append(kept, id)
			}
		}
		result.Config.Subscriptions[j].RouteIDs = kept
	}
	if decoded.Bundle.Mode == "replace" {
		result.Config.RouteEdits = nil
	} else {
		result.Config.RouteEdits = MinimalEdits(installed, result.Config.RouteEdits)
	}
	if decoded.Signature == "trusted_author" && len(decoded.Bundle.Updates) > 0 {
		result.Config.UpdateSources = decoded.Bundle.Updates
	}
	result.Config, err = result.Config.Normalize()
	return result, err
}

// legacyRoutes turns a v1 link's zone and key into its one soar route.
func legacyRoutes(c Config) []links.Route {
	p := links.SoarParams{Zone: c.Zone, Resolvers: c.Resolvers, Canaries: c.Canaries}
	p.Key, _ = base64.StdEncoding.DecodeString(c.Key)
	raw, _ := json.Marshal(p)
	var params map[string]any
	json.Unmarshal(raw, &params)
	return []links.Route{{ID: "legacy-dns", Type: "soar", Wire: "soar-v1", Priority: 100, IPv6: c.TunnelIPv6, Params: params}}
}
