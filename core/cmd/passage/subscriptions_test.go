package main

import (
	stdbytes "bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	links "github.com/mrgluek/passage-link/link"
	"github.com/mrgluek/passage/core/tunnel"
)

func TestDesktopRestoresSubscriptionRevision(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	id, read := stdbytes.Repeat([]byte{1}, 16), stdbytes.Repeat([]byte{2}, 32)
	route := links.Route{ID: "one", Type: "wss", Wire: "wss-v1", Priority: 100, Params: map[string]any{"endpoint": "wss://example.org/route", "key": base64.StdEncoding.EncodeToString(stdbytes.Repeat([]byte{3}, 32))}}
	bundle := links.Bundle{V: 2, Kind: "routes", Mode: "replace", Sub: id, Revision: 1, Signer: pub, Routes: []links.Route{route}}
	seed, err := links.SealSubFile(bundle, id, read, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := links.EncodeInvite(links.Invite{V: 2, Kind: "sub", ID: id, Name: "Example", Sources: []string{"https://example.org/sub"}, ReadKey: read, Required: []string{"sub"}, Seed: seed}, key)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := links.InviteURI(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, results, err := importLinks(uri)
	if err != nil || len(results) != 1 || len(cfg.Subscriptions) != 1 || len(cfg.Routes) != 1 {
		t.Fatal("seed import", err)
	}
	bundle.Revision = 2
	bundle.Routes[0].Priority = 50
	cfg, err = tunnel.ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, linkName), []byte(uri), 0600); err != nil {
		t.Fatal(err)
	}
	if err = tunnel.SaveSubscriptionState(filepath.Join(dir, "subscriptions.json"), cfg); err != nil {
		t.Fatal(err)
	}
	app := newApp(dir, "dev", "", "en", &fakePlatform{})
	if !app.hasLink() || app.linkCfg.Subscriptions[0].Revision != 2 || app.linkCfg.Routes[0].Priority != 50 {
		t.Fatal("cached routes not restored")
	}
}
