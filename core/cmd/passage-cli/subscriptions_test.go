package main

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	links "github.com/mrgluek/passage-link/link"
	"github.com/mrgluek/passage/core/tunnel"
)

func cliInvite(t *testing.T, n byte) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := links.EncodeInvite(links.Invite{V: 2, Kind: "sub", ID: bytes.Repeat([]byte{n}, 16), Name: "Example", Sources: []string{"https://example.org/sub"}, ReadKey: bytes.Repeat([]byte{3}, 32), Required: []string{"sub"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := links.InviteURI(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	return uri
}

func TestCLISubscriptionPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	cfg, exists, err := loadSubscriptions(path)
	if err != nil || exists {
		t.Fatal(exists, err)
	}
	first, err := tunnel.SubscribeLink(cfg, cliInvite(t, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tunnel.SaveSubscriptionState(path, first.Config); err != nil {
		t.Fatal(err)
	}
	cfg, exists, err = loadSubscriptions(path)
	if err != nil || !exists || len(cfg.Subscriptions) != 1 {
		t.Fatal(exists, err)
	}
	second, err := tunnel.SubscribeLink(cfg, cliInvite(t, 2), false)
	if err != nil || len(second.Config.Subscriptions) != 2 {
		t.Fatal("existing subscription lost", err)
	}
	if err = tunnel.SaveSubscriptionState(path, second.Config); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = loadSubscriptions(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = tunnel.RemoveSubscription(cfg, first.Config.Subscriptions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = tunnel.SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = loadSubscriptions(path)
	if err != nil || len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].ID == first.Config.Subscriptions[0].ID {
		t.Fatal("removal not saved", err)
	}
	cfg, err = tunnel.RemoveSubscription(cfg, cfg.Subscriptions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = tunnel.SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, exists, err = loadSubscriptions(path)
	if err != nil || !exists || len(cfg.Subscriptions) != 0 {
		t.Fatal("empty state not restored", err)
	}
	if err = os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, exists, err = loadSubscriptions(path)
	if err == nil || !exists || len(cfg.Subscriptions) != 0 {
		t.Fatal("invalid state accepted")
	}
}
