package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
	"github.com/mrgluek/passage/core/tunnel"
	"github.com/mrgluek/passage/core/update"
)

type updatePlatform struct {
	fakePlatform
	failure   error
	installed chan string
}

func (*updatePlatform) updateTarget() string { return "darwin-arm64" }
func (p *updatePlatform) applyUpdate(archive, version string) error {
	if p.installed != nil {
		p.installed <- version
	}
	return p.failure
}

type fakeUpdateTransport struct {
	offer   *update.Offer
	failure error
	wait    bool
}

func (f fakeUpdateTransport) Check(ctx context.Context, current, target string) (*update.Offer, error) {
	if f.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.offer, f.failure
}
func (f fakeUpdateTransport) Download(ctx context.Context, offer *update.Offer, dest string, progress func(int64, int64)) error {
	if f.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.failure != nil {
		return f.failure
	}
	progress(3, 3)
	return os.WriteFile(dest, []byte("zip"), 0600)
}

func waitUpdate(t *testing.T, a *App, state string) updateView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v := a.view().Update
		if v.State == state {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("update state: %+v, want %s", a.view().Update, state)
	return updateView{}
}

func testUpdateApp(t *testing.T) (*App, *updatePlatform) {
	t.Helper()
	old := update.PublicKey
	pub, _, _ := ed25519.GenerateKey(nil)
	update.PublicKey = base64.RawURLEncoding.EncodeToString(pub)
	t.Cleanup(func() { update.PublicKey = old })
	p := &updatePlatform{}
	a := newApp(t.TempDir(), "1.0.0", "", "en", p)
	return a, p
}

func TestUpdateDue(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		age                    time.Duration
		next                   time.Time
		enabled, running, want bool
	}{
		{5 * time.Minute, now, true, true, true}, {4 * time.Minute, now, true, true, false},
		{6 * time.Minute, now.Add(time.Second), true, true, false}, {6 * time.Minute, now, false, true, false}, {6 * time.Minute, now, true, false, false},
	} {
		if got := updateDue(now, now.Add(-c.age), c.next, c.enabled, c.running); got != c.want {
			t.Fatalf("%+v: %v", c, got)
		}
	}
	if updateDue(now, time.Time{}, time.Time{}, true, true) {
		t.Fatal("zero start")
	}
	for range 100 {
		d := nextUpdate(now).Sub(now)
		if d < 20*time.Hour || d >= 28*time.Hour {
			t.Fatal(d)
		}
	}
}

func TestUpdateDisabledAndOptOut(t *testing.T) {
	a, _ := testUpdateApp(t)
	update.PublicKey = ""
	if a.view().Update.Enabled || a.checkUpdate() == nil {
		t.Fatal("no key exposed updates")
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	update.PublicKey = base64.RawURLEncoding.EncodeToString(pub)
	if _, err := a.call("setAutoUpdate", []byte("false")); err != nil {
		t.Fatal(err)
	}
	s, _, err := loadSettings(a.path(iniName))
	if err != nil || s.AutoUpdate {
		t.Fatal(s, err)
	}
	a.runningSince = time.Now().Add(-time.Hour)
	a.updateTick(time.Now())
	if a.view().Update.State != "idle" {
		t.Fatal(a.view().Update)
	}
	if !defaultSettings().AutoUpdate {
		t.Fatal("default disabled")
	}
}

func TestUpdateFlowAndFailures(t *testing.T) {
	a, p := testUpdateApp(t)
	sum := sha256.Sum256([]byte("zip"))
	offer := &update.Offer{Version: "1.0.1", File: update.File{Name: "app.zip", Size: 3, SHA256: hex.EncodeToString(sum[:])}}
	a.updateTransport = fakeUpdateTransport{offer: offer}
	if err := a.checkUpdate(); err != nil {
		t.Fatal(err)
	}
	waitUpdate(t, a, "available")
	s, _, err := loadSettings(a.path(iniName))
	if err != nil || s.NextUpdate.Before(time.Now().Add(19*time.Hour)) {
		t.Fatal(s, err)
	}
	if err := a.downloadUpdate(); err != nil {
		t.Fatal(err)
	}
	waitUpdate(t, a, "ready")
	a.mu.Lock()
	archive := a.updater.archive
	a.mu.Unlock()
	t.Cleanup(func() { os.Remove(archive) })
	p.failure = errors.New("install failed")
	if err := a.installUpdate(); err != nil {
		t.Fatal(err)
	}
	if v := waitUpdate(t, a, "error"); v.Error != "install failed" || !v.Ready {
		t.Fatal(v)
	}
	os.WriteFile(archive, []byte("bad"), 0600)
	if err := a.installUpdate(); err != nil {
		t.Fatal(err)
	}
	if v := waitUpdate(t, a, "error"); v.Error == "install failed" {
		t.Fatal("tampered file accepted")
	}
	a.updateTransport = fakeUpdateTransport{failure: errors.New("check failed")}
	if err := a.checkUpdate(); err != nil {
		t.Fatal(err)
	}
	if v := waitUpdate(t, a, "error"); v.Error != a.t.f("update_check_failed") {
		t.Fatal(v)
	}
	a.updateTransport = fakeUpdateTransport{failure: errors.New("download failed")}
	if err := a.downloadUpdate(); err != nil {
		t.Fatal(err)
	}
	if v := waitUpdate(t, a, "error"); v.Error != a.t.f("update_download_failed") {
		t.Fatal(v)
	}
	a.updateTransport = fakeUpdateTransport{wait: true}
	if err := a.checkUpdate(); err != nil {
		t.Fatal(err)
	}
	if err := a.checkUpdate(); err == nil {
		t.Fatal("concurrent check")
	}
	a.cancelUpdate()
	waitUpdate(t, a, "cancelled")
	if err := a.downloadUpdate(); err != nil {
		t.Fatal(err)
	}
	a.cancelUpdate()
	waitUpdate(t, a, "cancelled")
	a.updateTransport = fakeUpdateTransport{}
	if err := a.checkUpdate(); err != nil {
		t.Fatal(err)
	}
	waitUpdate(t, a, "current")
	if err := a.installUpdate(); err == nil {
		t.Fatal("install without offer")
	}
	if _, err := os.Stat(filepath.Join(a.dir, iniName)); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticUpdateCheck(t *testing.T) {
	a, _ := testUpdateApp(t)
	a.settings.Mode = tunnel.ModeOff
	a.settings.Running = false
	a.setRunning(true)
	defer a.tun.Close()
	a.runningSince = time.Now().Add(-time.Hour)
	a.updateTransport = fakeUpdateTransport{offer: &update.Offer{Version: "1.0.1"}}
	a.settings.AutoUpdate = false
	a.updateTick(time.Now())
	if a.view().Update.State != "idle" {
		t.Fatal("opt-out checked")
	}
	a.settings.AutoUpdate = true
	a.updateTick(time.Now())
	v := waitUpdate(t, a, "available")
	if v.Done != 0 || v.Ready {
		t.Fatal("automatic download", v)
	}
	next := a.settings.NextUpdate
	a.updateTick(time.Now())
	if a.settings.NextUpdate != next {
		t.Fatal("checked before due")
	}
}

func TestDesktopLinkUpdateSources(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	old := tunnel.AuthorKey
	tunnel.AuthorKey = base64.RawURLEncoding.EncodeToString(public)
	defer func() { tunnel.AuthorKey = old }()
	bundle := links.Bundle{V: 2, Kind: "routes", Mode: "replace", Updates: []string{"https://example.org/repo"}, Routes: []links.Route{{
		ID: "dns", Type: "soar", Wire: "soar-v1", Priority: 10,
		Params: map[string]any{"zone": "t.example.org", "key": base64.StdEncoding.EncodeToString(make([]byte, 32))},
	}}}
	encode := func(key ed25519.PrivateKey) string {
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
	_, foreign, _ := ed25519.GenerateKey(nil)
	for _, c := range []struct {
		key  ed25519.PrivateKey
		want bool
	}{{private, true}, {foreign, false}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, linkName), []byte(encode(c.key)), 0600); err != nil {
			t.Fatal(err)
		}
		a := newApp(dir, "1.0.0", "", "en", &updatePlatform{})
		if a.linkErr != "" {
			t.Fatal(a.linkErr)
		}
		client := a.updateClientLocked().(*update.Client)
		found := false
		for _, source := range client.Sources {
			if source == "https://example.org/repo" {
				found = true
			}
		}
		if found != c.want {
			t.Fatal("trusted update source not preserved", client.Sources)
		}
	}
}
