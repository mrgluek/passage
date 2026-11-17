package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// managedInstall names the package manager that owns the folder of this
// program. Scoop writes install.json next to the files it installs.
func managedInstall(programDir string) string {
	if _, err := os.Stat(filepath.Join(programDir, "install.json")); err == nil {
		return "scoop"
	}
	return ""
}

// managedDataDir is where Scoop keeps the data of an app across versions,
// next to apps: settings must not stay in a version folder it deletes.
func managedDataDir(programDir string) string {
	if managedInstall(programDir) == "" {
		return ""
	}
	app := filepath.Dir(programDir)
	return filepath.Join(filepath.Dir(filepath.Dir(app)), "persist", filepath.Base(app))
}

// stableProgramPath is the path that survives an update by the package
// manager: Scoop keeps each version in its own folder and points "current"
// at the newest.
func stableProgramPath(exe string) string {
	dir := filepath.Dir(exe)
	if managedInstall(dir) == "" {
		return exe
	}
	current := filepath.Join(filepath.Dir(dir), "current", filepath.Base(exe))
	if strings.EqualFold(filepath.Base(dir), "current") {
		return exe
	}
	if _, err := os.Stat(current); err != nil {
		return exe
	}
	return current
}

var detectManaged = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return managedInstall(filepath.Dir(exe))
})
