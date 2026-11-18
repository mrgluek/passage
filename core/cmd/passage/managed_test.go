package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgluek/passage/core/update"
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
	defer func() { managedBy = old }()
	a, _ := testUpdateApp(t)
	a.updateTransport = fakeUpdateTransport{offer: &update.Offer{Version: "1.0.1"}}
	if err := a.checkUpdate(); err != nil {
		t.Fatal(err)
	}
	waitUpdate(t, a, "available")
	managedBy = func() string { return "scoop" }
	if v := a.view().Update; v.Managed != "scoop" {
		t.Fatalf("view: %+v", v)
	}
	if err := a.downloadUpdate(); err == nil {
		t.Fatal("a managed copy started a download")
	}
	if st := a.view().Update.State; st != "available" {
		t.Fatalf("state after the refusal: %s", st)
	}
	managedBy = func() string { return "" }
	if err := a.downloadUpdate(); err != nil {
		t.Fatalf("an unmanaged copy was refused: %v", err)
	}
	waitUpdate(t, a, "ready")
}
