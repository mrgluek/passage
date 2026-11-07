package main

import (
	stdbytes "bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func newSubscriptionTestApp(t *testing.T, lang string) *App {
	t.Helper()
	a := newApp(t.TempDir(), "dev", "", lang, &fakePlatform{})
	a.settings.Running = false
	a.saveSettings()
	return a
}

func desktopInvite(t *testing.T, key ed25519.PrivateKey, seed bool) string {
	t.Helper()
	id, read := stdbytes.Repeat([]byte{1}, 16), stdbytes.Repeat([]byte{2}, 32)
	var sealed []byte
	if seed {
		route := links.Route{ID: "one", Type: "wss", Wire: "wss-v1", Priority: 100, Params: map[string]any{"endpoint": "wss://example.org/route", "key": base64.StdEncoding.EncodeToString(stdbytes.Repeat([]byte{3}, 32))}}
		bundle := links.Bundle{V: 2, Kind: "routes", Mode: "replace", Sub: id, Revision: 1, Signer: key.Public().(ed25519.PublicKey), Routes: []links.Route{route}}
		var err error
		sealed, err = links.SealSubFile(bundle, id, read, key)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := links.EncodeInvite(links.Invite{V: 2, Kind: "sub", ID: id, Name: "Example", Sources: []string{"https://example.org/sub"}, ReadKey: read, Required: []string{"sub"}, Seed: sealed}, key)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := links.InviteURI(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	return uri
}

func TestDesktopSubscriptionPreviewAcceptRemove(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	invite := desktopInvite(t, key, true)
	a := newSubscriptionTestApp(t, "en")
	p, err := a.previewImport(invite)
	if err != nil || p.Mode != "subscribe" || p.Collision || !strings.Contains(p.Text, "Unknown signer") || !strings.Contains(p.Text, "first route list") || !strings.Contains(p.Text, "1 address") || a.hasLink() {
		t.Fatalf("preview %+v %v", p, err)
	}
	old := tunnel.AuthorKey
	tunnel.AuthorKey = base64.RawURLEncoding.EncodeToString(pub)
	t.Cleanup(func() { tunnel.AuthorKey = old })
	p, err = a.previewImport(invite)
	if err != nil || !strings.Contains(p.Text, "Signed by") {
		t.Fatalf("trusted %+v %v", p, err)
	}
	if err = a.saveLink(invite); err == nil {
		t.Fatal("invite bypassed confirmation path")
	}
	if err = a.subscribe(invite, false); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(a.path(linkName)); !os.IsNotExist(err) {
		t.Fatal("invite saved in link.txt")
	}
	if got := a.linkView(false).Routes; len(got) != 1 || got[0].Subscription != "Example" {
		t.Fatal(got)
	}
	if err = a.saveRouteEdits([]savedRoute{{ID: "one", Priority: 40, On: false}}); err != nil {
		t.Fatal(err)
	}
	b := newApp(a.dir, "dev", "", "en", &fakePlatform{})
	if !b.hasLink() || len(b.linkCfg.Subscriptions) != 1 || len(b.linkCfg.Routes) != 1 || !b.linkCfg.RouteEdits["one"].Off {
		t.Fatal("sidecar-only restart failed")
	}
	if err = b.removeSubscription(b.linkCfg.Subscriptions[0].ID); err != nil {
		t.Fatal(err)
	}
	c := newApp(a.dir, "dev", "", "en", &fakePlatform{})
	if c.hasLink() || len(c.linkCfg.RouteEdits) != 0 {
		t.Fatal("removal did not survive restart")
	}
	if err = c.removeSubscription("missing"); err == nil {
		t.Fatal("missing subscription accepted")
	}
}

func TestDesktopSubscriptionCollision(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	_, other, _ := ed25519.GenerateKey(nil)
	a := newSubscriptionTestApp(t, "en")
	if err := a.subscribe(desktopInvite(t, key, true), false); err != nil {
		t.Fatal(err)
	}
	incoming := desktopInvite(t, other, false)
	p, err := a.previewImport(incoming)
	if err != nil || !p.Collision || !strings.Contains(p.Text, "different signer") {
		t.Fatalf("%+v %v", p, err)
	}
	if err = a.subscribe(incoming, false); err == nil {
		t.Fatal("collision accepted")
	}
	if !stdbytes.Equal(a.linkCfg.Subscriptions[0].Signer, key.Public().(ed25519.PublicKey)) {
		t.Fatal("signer changed without consent")
	}
	if err = a.subscribe(incoming, true); err != nil {
		t.Fatal(err)
	}
	b := newApp(a.dir, "dev", "", "en", &fakePlatform{})
	if !stdbytes.Equal(b.linkCfg.Subscriptions[0].Signer, other.Public().(ed25519.PublicKey)) {
		t.Fatal("new signer not persisted")
	}
}

func TestDesktopSubscriptionRemoveLegacyInvite(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	dir := t.TempDir()
	invite := desktopInvite(t, key, true)
	if err := os.WriteFile(filepath.Join(dir, linkName), []byte(invite), 0600); err != nil {
		t.Fatal(err)
	}
	a := newApp(dir, "dev", "", "en", &fakePlatform{})
	a.settings.Running = false
	if err := a.removeSubscription(a.linkCfg.Subscriptions[0].ID); err != nil {
		t.Fatal(err)
	}
	b := newApp(dir, "dev", "", "en", &fakePlatform{})
	if b.hasLink() {
		t.Fatal("legacy invite resurrected removed subscription")
	}
}

func TestDesktopSubscriptionResults(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		tr := tr(table(lang))
		before := tunnel.Subscription{Revision: 1}
		if got := refreshResult(tr, before, tunnel.Subscription{Revision: 2}); got != tr.f("sub_updated") {
			t.Fatal(got)
		}
		for _, code := range []string{"", "unchanged"} {
			if got := refreshResult(tr, before, tunnel.Subscription{Revision: 1, LastError: code}); got != tr.f("sub_current") {
				t.Fatal(got)
			}
			a := newSubscriptionTestApp(t, lang)
			a.linkCfg.Subscriptions = []tunnel.Subscription{{Name: "Example", Revision: 1, LastOK: time.Now().Unix(), LastError: code}}
			if got := a.view().Subscriptions[0].Detail; !strings.Contains(got, tr.f("sub_just_now")) {
				t.Fatal(got)
			}
		}
		for _, code := range []string{"network", "http", "size", "decrypt", "signature", "rollback", "collision", "required"} {
			if got := refreshResult(tr, before, tunnel.Subscription{LastError: code}); got != tr.f("sub_failed", tr.f("sub_error_"+code)) {
				t.Fatal(got)
			}
		}
	}
	before := tunnel.Config{Routes: []links.Route{{ID: "one"}, {ID: "two"}}}
	after := tunnel.Config{Routes: []links.Route{{ID: "two"}, {ID: "three"}}}
	if a, r := routeChanges(before, after); a != 1 || r != 1 {
		t.Fatal(a, r)
	}
}

func TestDesktopManualImportDetachesSubscriptionRoutes(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	a := newSubscriptionTestApp(t, "en")
	if err := a.subscribe(desktopInvite(t, key, true), false); err != nil {
		t.Fatal(err)
	}
	if err := a.saveLink(v2Link(t, "merge", "one", 30)); err != nil {
		t.Fatal(err)
	}
	if len(a.linkCfg.Subscriptions[0].RouteIDs) != 0 || a.linkCfg.Routes[0].Priority != 30 {
		t.Fatal("manual import did not detach route")
	}
	if err := a.removeSubscription(a.linkCfg.Subscriptions[0].ID); err != nil {
		t.Fatal(err)
	}
	b := newApp(a.dir, "dev", "", "en", &fakePlatform{})
	if len(b.linkCfg.Routes) != 1 || b.linkCfg.Routes[0].Priority != 30 {
		t.Fatal("manual route removed")
	}
}

func TestDesktopSubscriptionNoSeedAndSaveFailure(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	a := newSubscriptionTestApp(t, "en")
	invite := desktopInvite(t, key, false)
	p, err := a.previewImport(invite)
	if err != nil || !strings.Contains(p.Text, "Routes will be loaded") {
		t.Fatalf("%+v %v", p, err)
	}
	if err = a.subscribe(invite, false); err != nil {
		t.Fatal(err)
	}
	sub := a.linkCfg.Subscriptions[0]
	// A directory at the state path makes the atomic replacement fail.
	if err = os.Remove(a.path("subscriptions.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(a.path("subscriptions.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = a.removeSubscription(sub.ID); err == nil {
		t.Fatal("save failure ignored")
	}
	if len(a.linkCfg.Subscriptions) != 1 {
		t.Fatal("failed removal applied")
	}
}

func TestDesktopRemoveSubscriptionRestartsTunnel(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	a := newSubscriptionTestApp(t, "en")
	a.settings.Mode = tunnel.ModeOff
	if err := a.subscribe(desktopInvite(t, key, true), false); err != nil {
		t.Fatal(err)
	}
	a.setRunning(true)
	defer a.setRunning(false)
	old := a.tun
	if old == nil {
		t.Fatal(a.lastErr)
	}
	if err := a.removeSubscription(a.linkCfg.Subscriptions[0].ID); err != nil {
		t.Fatal(err)
	}
	if a.tun == nil || a.tun == old || len(a.linkCfg.Routes) != 0 {
		t.Fatal("tunnel was not restarted")
	}
}

func TestClosedSubscriptionRow(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		a := &App{t: tr(table(lang)), linkCfg: tunnel.Config{Subscriptions: []tunnel.Subscription{{ID: "example", Name: "Example", Closed: true, Revision: 2, LastError: "network", LastOK: 1}}}, subscriptionRefreshing: true, subscriptionResults: map[string]string{"example": "old result"}}
		rows := a.subscriptionRowsLocked()
		if len(rows) != 1 || !rows[0].Closed || rows[0].Detail != a.t.f("sub_closed") || rows[0].Busy || rows[0].Result != "" {
			t.Fatal(rows)
		}
	}
}

func TestRoutesNoticeTimeAndLegacy(t *testing.T) {
	before := tunnel.Config{Routes: []links.Route{{ID: "a"}, {ID: "b"}}}
	after := tunnel.Config{Routes: []links.Route{{ID: "b", Priority: 20}, {ID: "c"}, {ID: "c"}}}
	stamp := time.Date(2026, 11, 6, 7, 5, 0, 0, time.Local)
	value := refreshNotice(before, after, stamp)
	if want := "1,1," + fmt.Sprint(stamp.UnixMilli()); value != want {
		t.Fatal(value, want)
	}
	for _, lang := range []string{"en", "ru"} {
		tr := tr(table(lang))
		if got := noticeText(tr, value); got != tr.f("sub_routes_timed", 1, 1, "07:05") {
			t.Fatal(got)
		}
		if got := noticeText(tr, "1,2"); got != tr.f("sub_routes_updated", 1, 2) {
			t.Fatal(got)
		}
		for _, bad := range []string{"", "1", "a,2", "-1,0", "1,2,bad", "1,2,0", "1,2,3,4"} {
			if got := noticeText(tr, bad); got != "" {
				t.Fatal(bad, got)
			}
		}
	}
}

func TestClosingRefreshNoticePersistsAndDismisses(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	a := newSubscriptionTestApp(t, "en")
	if err := a.subscribe(desktopInvite(t, key, true), false); err != nil {
		t.Fatal(err)
	}
	sub := a.linkCfg.Subscriptions[0]
	closed, err := tunnel.ApplySubFile(a.linkCfg, sub, links.Bundle{V: 2, Kind: "routes", Mode: "replace", Signer: key.Public().(ed25519.PublicKey), Sub: stdbytes.Repeat([]byte{1}, 16), Revision: 2, Closed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.applySubscriptionRefreshLocked(closed, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.settings.RoutesNotice, "0,1,") {
		t.Fatal(a.settings.RoutesNotice)
	}
	if !strings.Contains(a.view().SubscriptionNotice, "Routes updated: +0, -1 · ") {
		t.Fatal(a.view().SubscriptionNotice)
	}
	b := newApp(a.dir, "dev", "", "en", &fakePlatform{})
	if b.view().SubscriptionNotice != a.view().SubscriptionNotice {
		t.Fatal("notice lost on restart")
	}
	old := b.settings.RoutesNotice
	metadata := b.linkCfg
	metadata.Subscriptions = append([]tunnel.Subscription(nil), metadata.Subscriptions...)
	metadata.Subscriptions[0].LastError = "unchanged"
	if err = b.applySubscriptionRefreshLocked(metadata, false); err != nil {
		t.Fatal(err)
	}
	if b.settings.RoutesNotice != old {
		t.Fatal("metadata replaced notice")
	}
	if _, err = b.call("dismissSubscriptionNotice", nil); err != nil {
		t.Fatal(err)
	}
	c := newApp(a.dir, "dev", "", "en", &fakePlatform{})
	if c.view().SubscriptionNotice != "" {
		t.Fatal("dismissal not persisted")
	}
	raw, err := os.ReadFile(a.path(logName))
	if err != nil || strings.Count(string(raw), "routes updated: +0, -1") != 1 {
		t.Fatal(string(raw), err)
	}
	c.settings.RoutesNotice = old
	if err = c.removeSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	if c.settings.RoutesNotice != "" {
		t.Fatal("removal kept stale notice")
	}
}
