// Command passage-update-manifest makes the update key and signs a release's
// manifest. The key comes from the PASSAGE_UPDATE_KEY environment variable;
// PASSAGE_UPDATE_PUBLIC_KEY, if set, must be its public half.
//
//	passage-update-manifest keygen
//	passage-update-manifest sign <version> <dir>
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mrgluek/passage/core/update"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 2 && os.Args[1] == "keygen":
		err = keygen()
	case len(os.Args) == 4 && os.Args[1] == "sign":
		err = sign(os.Args[2], os.Args[3])
	default:
		err = errors.New("usage: passage-update-manifest keygen | sign <version> <dir>")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func keygen() error {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	fmt.Println("PASSAGE_UPDATE_KEY=" + base64.RawURLEncoding.EncodeToString(private.Seed()))
	fmt.Println("PASSAGE_UPDATE_PUBLIC_KEY=" + base64.RawURLEncoding.EncodeToString(public))
	return nil
}

func sign(version, dir string) error {
	seed, err := base64.RawURLEncoding.DecodeString(os.Getenv("PASSAGE_UPDATE_KEY"))
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("PASSAGE_UPDATE_KEY is missing or invalid")
	}
	key := ed25519.NewKeyFromSeed(seed)
	public := base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	if want := os.Getenv("PASSAGE_UPDATE_PUBLIC_KEY"); want != "" && want != public {
		return errors.New("PASSAGE_UPDATE_KEY does not match PASSAGE_UPDATE_PUBLIC_KEY")
	}
	m := update.Manifest{Version: version, Files: map[string]update.File{}}
	for _, target := range []string{"arm64", "armv7", "windows-amd64", "darwin-arm64", "darwin-amd64"} {
		ext := "zip"
		if target == "arm64" || target == "armv7" {
			ext = "apk"
		}
		file, err := describe(dir, fmt.Sprintf("passage-%s-%s.%s", version, target, ext))
		if err != nil {
			return err
		}
		m.Files[target] = file
	}
	raw, err := update.Sign(m, key)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, update.ManifestName), raw, 0o644)
}

func describe(dir, name string) (update.File, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return update.File{}, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return update.File{}, err
	}
	return update.File{Name: name, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
