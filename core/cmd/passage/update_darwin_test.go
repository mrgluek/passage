//go:build darwin && cgo

package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestMacArchivePaths(t *testing.T) {
	for _, name := range []string{"../escape", "/escape", "Passage.app/../../escape", "Passage.app/Contents/MacOS/passage"} {
		file := filepath.Join(t.TempDir(), "app.zip")
		f, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		z := zip.NewWriter(f)
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("app"))
		z.Close()
		f.Close()
		err = safeMacArchive(file)
		if (name == "Passage.app/Contents/MacOS/passage") != (err == nil) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func signedTestBundle(t *testing.T, identifier, version string) string {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "Passage.app")
	bin := filepath.Join(bundle, "Contents", "MacOS")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"passage", "passage-iroh"} {
		src, err := os.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		dest, err := os.OpenFile(filepath.Join(bin, name), os.O_CREATE|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(dest, src)
		src.Close()
		dest.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := command("/usr/bin/codesign", "--force", "--sign", "-", filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	plist := fmt.Sprintf(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>%s</string><key>CFBundleVersion</key><string>%s</string><key>CFBundleShortVersionString</key><string>%s</string><key>CFBundleExecutable</key><string>passage</string></dict></plist>`, identifier, version, version)
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	if err := command("/usr/bin/codesign", "--force", "--sign", "-", bundle); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestMacBundleVerification(t *testing.T) {
	bundle := signedTestBundle(t, "info.gluek.passage", "1.0.1")
	if err := verifyMacBundle(bundle, "1.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := verifyMacBundle(bundle, "1.0.2"); err == nil {
		t.Fatal("wrong version accepted")
	}
	wrong := signedTestBundle(t, "org.example.other", "1.0.1")
	if err := verifyMacBundle(wrong, "1.0.1"); err == nil {
		t.Fatal("wrong identifier accepted")
	}
	f, err := os.OpenFile(filepath.Join(bundle, "Contents", "MacOS", "passage"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("changed")
	f.Close()
	if err := verifyMacBundle(bundle, "1.0.1"); err == nil {
		t.Fatal("invalid signature accepted")
	}
}
