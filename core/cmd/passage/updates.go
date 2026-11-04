package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrgluek/passage/core/update"
)

type updateTransport interface {
	Check(context.Context, string, string) (*update.Offer, error)
	Download(context.Context, *update.Offer, string, func(int64, int64)) error
}

type desktopUpdate struct {
	state       string
	err         string
	offer       *update.Offer
	done, total int64
	archive     string
	cancel      context.CancelFunc
}

type updateView struct {
	Ready   bool   `json:"ready"`
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	Version string `json:"version"`
	Error   string `json:"error"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
}

func (a *App) updatesEnabled() bool {
	key, err := base64.RawURLEncoding.DecodeString(update.PublicKey)
	return err == nil && len(key) == ed25519.PublicKeySize && a.os.updateTarget() != ""
}

func (a *App) updateViewLocked() updateView {
	u := a.updater
	v := updateView{Ready: u.archive != "" && u.cancel == nil && u.state != "installing", Enabled: a.updatesEnabled(), State: u.state, Error: u.err, Done: u.done, Total: u.total}
	if v.State == "" {
		v.State = "idle"
	}
	if u.offer != nil {
		v.Version = u.offer.Version
	}
	return v
}

func updateDue(now, runningSince, next time.Time, enabled, running bool) bool {
	return enabled && running && !runningSince.IsZero() && now.Sub(runningSince) >= 5*time.Minute && !now.Before(next)
}

func nextUpdate(now time.Time) time.Time {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(8*time.Hour)))
	if err != nil {
		return now.Add(24 * time.Hour)
	}
	return now.Add(20*time.Hour + time.Duration(n.Int64()))
}

func (a *App) updateTick(now time.Time) {
	a.mu.Lock()
	due := updateDue(now, a.runningSince, a.settings.NextUpdate, a.settings.AutoUpdate && a.updatesEnabled(), a.settings.Running && a.tun != nil)
	if due && a.updater.cancel == nil && a.updater.state != "installing" {
		a.beginCheckLocked(now)
	}
	a.mu.Unlock()
}

func (a *App) updateClientLocked() updateTransport {
	if a.updateTransport != nil {
		return a.updateTransport
	}
	key, _ := base64.RawURLEncoding.DecodeString(update.PublicKey)
	return &update.Client{Key: key, Sources: update.Sources(a.linkCfg.UpdateSources), Dials: func() []update.Dial {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.tun != nil {
			return a.tun.Dials()
		}
		return nil
	}}
}

func (a *App) checkUpdate() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.updatesEnabled() {
		return errors.New("updates unavailable")
	}
	if a.updater.cancel != nil || a.updater.state == "installing" {
		return errors.New("update already in progress")
	}
	a.beginCheckLocked(time.Now())
	return nil
}

func (a *App) beginCheckLocked(now time.Time) {
	a.settings.NextUpdate = nextUpdate(now)
	a.saveSettings()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	a.updater.state, a.updater.err, a.updater.cancel = "checking", "", cancel
	client, target := a.updateClientLocked(), a.os.updateTarget()
	go func() {
		offer, err := client.Check(ctx, a.version, target)
		cancel()
		a.mu.Lock()
		a.updater.cancel = nil
		switch {
		case errors.Is(err, context.Canceled):
			a.updater.state = "cancelled"
		case err != nil:
			a.event("update check failed: %v", err)
			a.updater.state, a.updater.err = "error", a.t.f("update_check_failed")
		default:
			keep := offer != nil && a.updater.offer != nil && offer.Version == a.updater.offer.Version && offer.File == a.updater.offer.File
			a.updater.offer = offer
			if !keep {
				a.updater.archive = ""
				a.updater.done, a.updater.total = 0, 0
			}
			a.updater.state = "current"
			if offer != nil {
				a.updater.state = "available"
			}
			if a.updater.archive != "" {
				a.updater.state = "ready"
			}
		}
		a.mu.Unlock()
		a.changed()
	}()
	a.changedAfter()
}

func (a *App) downloadUpdate() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.updatesEnabled() || a.updater.offer == nil {
		return errors.New("no update available")
	}
	if a.updater.cancel != nil || a.updater.state == "installing" {
		return errors.New("update already in progress")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	configID := sha256.Sum256([]byte(a.dir))
	cache = filepath.Join(cache, "Passage", "updates", hex.EncodeToString(configID[:8]))
	if err = os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	offer, client := a.updater.offer, a.updateClientLocked()
	dest := filepath.Join(cache, offer.File.SHA256+".zip")
	ctx, cancel := context.WithCancel(context.Background())
	a.updater.state, a.updater.err, a.updater.cancel = "downloading", "", cancel
	a.updater.done, a.updater.total = 0, offer.File.Size
	go func() {
		err := client.Download(ctx, offer, dest, func(done, total int64) {
			a.mu.Lock()
			a.updater.done, a.updater.total = done, total
			a.mu.Unlock()
		})
		cancel()
		a.mu.Lock()
		a.updater.cancel = nil
		switch {
		case errors.Is(err, context.Canceled):
			a.updater.state = "cancelled"
		case err != nil:
			a.event("update download failed: %v", err)
			a.updater.state, a.updater.err = "error", a.t.f("update_download_failed")
		default:
			a.updater.state, a.updater.archive = "ready", dest
		}
		a.mu.Unlock()
		a.changed()
	}()
	a.changedAfter()
	return nil
}

func (a *App) cancelUpdate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updater.cancel != nil {
		a.updater.cancel()
	}
}

func (a *App) installUpdate() error {
	a.mu.Lock()
	if !a.updatesEnabled() || a.updater.archive == "" || a.updater.offer == nil || a.updater.cancel != nil || a.updater.state == "installing" {
		a.mu.Unlock()
		return errors.New("download the update first")
	}
	archive, version, file := a.updater.archive, a.updater.offer.Version, a.updater.offer.File
	a.updater.state, a.updater.err = "installing", ""
	a.mu.Unlock()
	a.changed()
	go func() {
		err := verifyUpdateArchive(archive, file)
		verified := err == nil
		if err == nil {
			err = a.os.applyUpdate(archive, version)
		}
		if err != nil {
			a.mu.Lock()
			a.updater.state, a.updater.err = "error", err.Error()
			if !verified {
				a.updater.archive = ""
			}
			a.mu.Unlock()
			a.changed()
		}
	}()
	return nil
}

func verifyUpdateArchive(archive string, want update.File) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != want.Size || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want.SHA256) {
		return errors.New("downloaded file does not match the manifest")
	}
	return nil
}
