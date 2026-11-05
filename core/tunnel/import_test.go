package tunnel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	links "github.com/mrgluek/passage-link/link"
)

func TestImportV2PreservesRoutesAndRejectsBadReplacement(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	old := AuthorKey
	AuthorKey = base64.RawURLEncoding.EncodeToString(pub)
	defer func() { AuthorKey = old }()
	makeRoute := func(id string) links.Route {
		return links.Route{ID: id, Type: "soar", Wire: "soar-v1", Priority: 100, Params: map[string]any{"zone": "tunnel.example.org", "key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}}
	}
	makeLink := func(mode string, r links.Route) string {
		raw, err := links.Encode(links.Bundle{V: 2, Kind: "routes", Mode: mode, Routes: []links.Route{r}}, key)
		if err != nil {
			t.Fatal(err)
		}
		uri, _ := links.URI(raw, "")
		return uri
	}
	existing := Config{Routes: []links.Route{makeRoute("first")}}
	result, err := ImportLink(existing, makeLink("merge", makeRoute("second")), false)
	if err != nil || result.Signature != "trusted_author" || len(result.Config.Routes) != 2 {
		t.Fatal("merge failed", err)
	}
	r := makeRoute("first")
	r.Priority = 1
	preview, err := ImportLink(existing, makeLink("merge", r), false)
	if err != nil || !preview.Collision {
		t.Fatal("missing collision preview", err)
	}
	result, err = ImportLink(existing, makeLink("replace", makeRoute("second")), true)
	if err != nil || len(result.Config.Routes) != 1 || result.Config.Routes[0].ID != "second" {
		t.Fatal("replace failed", err)
	}
	raw, _ := links.Encode(links.Bundle{V: 2, Kind: "routes", Mode: "replace", Routes: []links.Route{r}}, key)
	raw[len(raw)-1] ^= 1
	uri, _ := links.URI(raw, "")
	if _, err = ImportLink(existing, uri, true); err == nil || existing.Routes[0].Priority != 100 {
		t.Fatal("invalid replacement changed routes")
	}
}

func TestImportLegacyAndClear(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	legacy := "passage://tunnel?v=1&zone=tunnel.example.org&key=" + key
	first, err := ImportLink(Config{}, legacy, true)
	if err != nil || !first.Config.HasTunnel() {
		t.Fatal("legacy import failed", err)
	}
	cleared, err := ImportLink(first.Config, "", true)
	if err != nil || cleared.Config.HasTunnel() || cleared.Mode != "replace" {
		t.Fatal("clear failed", err)
	}
}

func TestImportUpdateSources(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle := links.Bundle{V: 2, Kind: "routes", Mode: "replace", Updates: []string{"https://example.org/repo"}, Routes: []links.Route{{
		ID: "dns", Type: "soar", Wire: "soar-v1", Priority: 10,
		Params: map[string]any{"zone": "t.example.org", "key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))},
	}}}
	sign := func(key ed25519.PrivateKey) string {
		raw, err := links.Encode(bundle, key)
		if err != nil {
			t.Fatal(err)
		}
		uri, err := links.URI(raw, "")
		if err != nil {
			t.Fatal(err)
		}
		return uri
	}
	old := AuthorKey
	AuthorKey = base64.RawURLEncoding.EncodeToString(public)
	defer func() { AuthorKey = old }()
	trusted, err := ImportLink(Config{}, sign(private), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(trusted.Config.UpdateSources) != 1 {
		t.Fatalf("trusted link: %v", trusted.Config.UpdateSources)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	foreign, err := ImportLink(Config{}, sign(other), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign.Config.UpdateSources) != 0 {
		t.Fatalf("foreign link: %v", foreign.Config.UpdateSources)
	}
}
