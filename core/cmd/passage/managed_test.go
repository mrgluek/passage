package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedInstall(t *testing.T) {
	root := t.TempDir()
	version := filepath.Join(root, "apps", "passage", "0.9.3")
	current := filepath.Join(root, "apps", "passage", "current")
	for _, dir := range []string{version, current} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "passage.exe"), nil, 0o644)
	}
	exe := filepath.Join(version, "passage.exe")
	if managedInstall(version) != "" || stableProgramPath(exe) != exe {
		t.Fatal("plain folder treated as managed")
	}
	os.WriteFile(filepath.Join(version, "scoop-install.json"), []byte(`{"bucket":"passage"}`), 0o644)
	if managedInstall(version) != "scoop" {
		t.Fatal("Scoop folder not detected")
	}
	if got, want := managedDataDir(version), filepath.Join(root, "persist", "passage"); got != want {
		t.Fatalf("data dir = %s, want %s", got, want)
	}
	if got := stableProgramPath(exe); got != filepath.Join(current, "passage.exe") {
		t.Fatalf("stable path = %s", got)
	}
	os.WriteFile(filepath.Join(current, "install.json"), nil, 0o644) // older Scoop name
	if got := stableProgramPath(filepath.Join(current, "passage.exe")); !strings.HasSuffix(got, filepath.Join("current", "passage.exe")) {
		t.Fatalf("current path changed: %s", got)
	}
	os.Remove(filepath.Join(current, "passage.exe"))
	if got := stableProgramPath(exe); got != exe {
		t.Fatalf("missing current: %s", got)
	}
}

func TestManagedInstallRefusesDownload(t *testing.T) {
	old := managedBy
	managedBy = func() string { return "scoop" }
	defer func() { managedBy = old }()
	if v := (&App{}).updateViewLocked(); v.Managed != "scoop" {
		t.Fatalf("view: %+v", v)
	}
}
