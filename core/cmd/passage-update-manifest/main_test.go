package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/mrgluek/passage/core/update"
)

func TestSignDesktopTargets(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PASSAGE_UPDATE_KEY", base64.RawURLEncoding.EncodeToString(private.Seed()))
	dir := t.TempDir()
	names := []string{"passage-1.0.1-arm64.apk", "passage-1.0.1-armv7.apk", "passage-1.0.1-windows-amd64.zip", "passage-1.0.1-darwin-arm64.zip", "passage-1.0.1-darwin-amd64.zip"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := sign("1.0.1", dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, update.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := update.Parse(raw, public)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"arm64", "armv7", "windows-amd64", "darwin-arm64", "darwin-amd64"} {
		if manifest.Files[target].Size == 0 {
			t.Fatal("missing", target)
		}
	}
	t.Setenv("PASSAGE_UPDATE_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString(public))
	if err := sign("1.0.1", dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PASSAGE_UPDATE_PUBLIC_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)))
	if err := sign("1.0.1", dir); err == nil {
		t.Fatal("mismatched public key accepted")
	}
	t.Setenv("PASSAGE_UPDATE_PUBLIC_KEY", "")
	for _, name := range names {
		file := filepath.Join(dir, name)
		os.Remove(file)
		if err := sign("1.0.1", dir); err == nil {
			t.Fatal("missing file accepted", name)
		}
		os.WriteFile(file, []byte(name), 0600)
	}
}
