package tunnel

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	links "github.com/mrgluek/passage-link/link"
)

// AuthorKey is supplied by the local release build, never by a link.
var AuthorKey string

type ImportResult struct {
	Config    Config   `json:"config"`
	Signature string   `json:"signature"`
	Mode      string   `json:"mode"`
	Count     int      `json:"count"`
	IDs       []string `json:"route_ids"`
	Collision bool     `json:"collision"`
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
	var trusted []ed25519.PublicKey
	if AuthorKey != "" {
		pub, err := base64.RawURLEncoding.DecodeString(AuthorKey)
		if err != nil || len(pub) != 32 {
			return result, errors.New("invalid build author key")
		}
		trusted = append(trusted, ed25519.PublicKey(pub))
	}
	decoded, err := links.ParseURI(text, trusted)
	if err != nil {
		return result, err
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
