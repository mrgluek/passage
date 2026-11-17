package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// managedInstall names the package manager that owns the folder of this
// program. Scoop writes scoop-install.json (install.json in older versions) next to the files it installs.
func managedInstall(programDir string) string {
	for _, name := range []string{"scoop-install.json", "install.json"} {
		if _, err := os.Stat(filepath.Join(programDir, name)); err == nil {
			return "scoop"
		}
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

// programExe is this program's path with links resolved. When a link cannot
// be resolved (a Scoop "current" junction on some systems) the path as
// started is still usable.
func programExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		return p, nil
	}
	return exe, nil
}

var detectManaged = sync.OnceValue(func() string {
	exe, err := programExe()
	if err != nil {
		return ""
	}
	return managedInstall(filepath.Dir(exe))
})
