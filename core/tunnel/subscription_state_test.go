package tunnel

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	links "github.com/mrgluek/passage-link/link"
)

func TestSubscriptionStatePersistsRevisionAndOwnership(t *testing.T) {
	sub, _, bundle := subFixture(t)
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Subscriptions[0].ETag = `"saved"`
	cfg.Subscriptions[0].ETagSource = sub.Sources[0]
	cfg.RouteEdits = map[string]RouteEdit{"one": {Off: true}}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err = SaveSubscriptionState(path, cfg); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0600 {
		t.Fatalf("state permissions %v", fi.Mode().Perm())
	}
	restored, err := RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub}}, path)
	if err != nil || len(restored.Routes) != 1 || restored.Subscriptions[0].Revision != 1 || !restored.RouteEdits["one"].Off {
		t.Fatal("restore failed", err)
	}
	sub.ReadKey = bytes.Repeat([]byte{9}, 32)
	restored, err = RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub}}, path)
	if err != nil || restored.Subscriptions[0].Revision != 1 || !bytes.Equal(restored.Subscriptions[0].ReadKey, sub.ReadKey) || restored.Subscriptions[0].ETag != "" {
		t.Fatal("key rotation reset revision", err)
	}
	sub.Sources = []string{"https://example.org/new"}
	manual := subRoute("one")
	manual.Priority = 5
	restored, err = RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub}, Routes: []links.Route{manual}}, path)
	if err != nil || len(restored.Routes) != 1 || restored.Routes[0].Priority != 5 || len(restored.Subscriptions[0].RouteIDs) != 0 || restored.Subscriptions[0].ETag != "" {
		t.Fatal("manual collision or sources", err)
	}
	sub.Signer = make([]byte, 32)
	restored, err = RestoreSubscriptionState(Config{Subscriptions: []Subscription{sub}}, path)
	if err != nil || len(restored.Routes) != 0 || restored.Subscriptions[0].Revision != 0 {
		t.Fatal("restored foreign signer", err)
	}
}
