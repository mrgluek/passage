//go:build darwin && cgo

package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"fyne.io/systray"
	"golang.org/x/sys/unix"
)

func (macPlatform) updateTarget() string { return "darwin-" + runtime.GOARCH }

func command(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(name), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func plistValue(bundle, key string) (string, error) {
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :"+key, filepath.Join(bundle, "Contents", "Info.plist")).Output()
	return strings.TrimSpace(string(out)), err
}

func verifyMacBundle(bundle, version string) error {
	var err error
	bundle, err = filepath.EvalSymlinks(bundle)
	if err != nil {
		return err
	}
	if err := command("/usr/bin/codesign", "--verify", "--deep", "--strict", bundle); err != nil {
		return err
	}
	for key, want := range map[string]string{"CFBundleIdentifier": "info.gluek.passage", "CFBundleShortVersionString": version, "CFBundleVersion": version, "CFBundleExecutable": "passage"} {
		got, err := plistValue(bundle, key)
		if err != nil || got != want {
			return fmt.Errorf("unexpected %s in update", key)
		}
	}
	arch := "arm64"
	if runtime.GOARCH == "amd64" {
		arch = "x86_64"
	}
	for _, name := range []string{"passage", "passage-iroh"} {
		exe := filepath.Join(bundle, "Contents", "MacOS", name)
		resolved, err := filepath.EvalSymlinks(exe)
		if err != nil || !strings.HasPrefix(resolved, bundle+string(os.PathSeparator)) {
			return errors.New("invalid update executable")
		}
		if err := command("/usr/bin/lipo", exe, "-verify_arch", arch); err != nil {
			return err
		}
	}
	return nil
}

func safeMacArchive(archive string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	var total uint64
	for _, f := range z.File {
		name := path.Clean(f.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return errors.New("invalid update archive path")
		}
		if f.UncompressedSize64 > 2<<30-total {
			return errors.New("update archive too large")
		}
		total += f.UncompressedSize64
		if f.Mode()&os.ModeSymlink != 0 {
			return errors.New("update archive contains a symbolic link")
		}
	}
	return nil
}

func (p macPlatform) applyUpdate(archive, version string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(bundle, ".app") || filepath.Base(filepath.Dir(exe)) != "MacOS" || filepath.Base(filepath.Dir(filepath.Dir(exe))) != "Contents" {
		p.open(filepath.Dir(archive))
		return errors.New(macApp.t.f("update_manual_bundle"))
	}
	parent := filepath.Dir(bundle)
	if unix.Access(parent, unix.W_OK|unix.X_OK) != nil || unix.Access(bundle, unix.W_OK) != nil {
		p.open(filepath.Dir(archive))
		return errors.New(macApp.t.f("update_manual_writable"))
	}
	if err := safeMacArchive(archive); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".Passage-update-")
	if err != nil {
		return err
	}
	preserve := false
	defer func() {
		if !preserve {
			os.RemoveAll(stage)
		}
	}()
	if err := command("/usr/bin/ditto", "-x", "-k", "--noqtn", archive, stage); err != nil {
		return err
	}
	candidate := filepath.Join(stage, "Passage.app")
	if err := verifyMacBundle(candidate, version); err != nil {
		return err
	}
	backup := candidate
	login := p.autostart()
	if err := unix.RenamexNp(bundle, candidate, unix.RENAME_SWAP); err != nil {
		return err
	}
	preserve = true
	restore := func(cause error) error {
		if err := unix.RenamexNp(bundle, backup, unix.RENAME_SWAP); err != nil {
			return fmt.Errorf("%v; previous app remains at %s: %w", cause, backup, err)
		}
		preserve = false
		if login {
			p.setAutostart(true)
		}
		return cause
	}
	if login {
		if err := p.setAutostart(true); err != nil {
			return restore(err)
		}
	}
	// Release passage.lock before the new process can start.
	macStop()
	child := exec.Command(filepath.Join(bundle, "Contents", "MacOS", "passage"), "-dir", macApp.dir)
	if err := child.Start(); err != nil {
		err = restore(err)
		old := exec.Command(filepath.Join(bundle, "Contents", "MacOS", "passage"), "-dir", macApp.dir)
		if launchErr := old.Start(); launchErr == nil {
			old.Process.Release()
		}
		systray.Quit()
		return err
	}
	child.Process.Release()
	preserve = false
	systray.Quit()
	return nil
}
