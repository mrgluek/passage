package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

const (
	// updateHealthEnv names the event a new version sets once it has run for
	// updateHealthDelay. Until then the old process can bring itself back.
	updateHealthEnv     = "PASSAGE_UPDATE_HEALTH"
	updateHealthDelay   = 10 * time.Second
	updateHealthTimeout = time.Minute
)

var (
	winApp        *App
	pendingUpdate atomic.Pointer[windowsUpdate]
)

// windowsUpdate is an installed update waiting for this process to shut down.
type windowsUpdate struct {
	exe, programDir, dir string
}

func (winPlatform) updateTarget() string {
	if runtime.GOARCH == "amd64" {
		return "windows-amd64"
	}
	return ""
}

func programPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func (p winPlatform) applyUpdate(archive, version string) error {
	exe, err := programPath()
	if err != nil {
		return err
	}
	programDir := filepath.Dir(exe)
	manual := func(key string) error {
		p.open(filepath.Dir(archive))
		return errors.New(winApp.t.f(key))
	}
	if insideArchive(programDir, os.TempDir()) {
		return manual("update_manual_archive_windows")
	}
	if !strings.EqualFold(filepath.Base(exe), "passage.exe") {
		return manual("update_manual_writable_windows")
	}
	files, err := validateWindowsArchive(archive, version)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(programDir, ".passage-update-")
	if err != nil {
		return manual("update_manual_writable_windows")
	}
	defer os.RemoveAll(stage)
	if err := extractAndVerifyWindowsUpdate(archive, version, stage, files); err != nil {
		return err
	}
	if _, err := executeReplacements(planReplacements(programDir, stage, files)); err != nil {
		return err
	}
	pendingUpdate.Store(&windowsUpdate{exe: exe, programDir: programDir, dir: winApp.dir})
	systray.Quit()
	return nil
}

func startPassage(u *windowsUpdate, env ...string) (*exec.Cmd, error) {
	cmd := exec.Command(u.exe, "-dir", u.dir)
	cmd.Dir = u.programDir
	cmd.Env = append(os.Environ(), env...)
	allowForeground.Call(0xFFFFFFFF) // ASFW_ANY: the new window may come forward
	return cmd, cmd.Start()
}

// finishUpdate runs after shutdown: the single-instance event must be closed
// first. It starts the new version and watches it until it proves itself.
func finishUpdate(u *windowsUpdate, t tr) {
	name := fmt.Sprintf(`Local\PassageUpdateHealth-%d`, os.Getpid())
	event, err := eventNamed(name)
	if err != nil {
		restoreUpdate(u, t)
		return
	}
	defer windows.CloseHandle(event)
	cmd, err := startPassage(u, updateHealthEnv+"="+name)
	if err != nil {
		restoreUpdate(u, t)
		return
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	healthy := make(chan struct{})
	go func() {
		if r, err := windows.WaitForSingleObject(event, uint32(updateHealthTimeout.Milliseconds())); err == nil && r == windows.WAIT_OBJECT_0 {
			close(healthy)
		}
	}()
	if awaitHealth(healthy, exited, updateHealthTimeout) {
		return
	}
	cmd.Process.Kill()
	<-exited
	restoreUpdate(u, t)
}

func eventNamed(name string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.CreateEvent(nil, 1, 0, p)
}

func restoreUpdate(u *windowsUpdate, t tr) {
	if err := rollbackUpdate(u.programDir); err != nil {
		messageBox(t.f("update_restore_failed", u.programDir), windows.MB_OK|windows.MB_ICONERROR)
		return
	}
	if cmd, err := startPassage(u); err == nil {
		cmd.Process.Release()
	}
	messageBox(t.f("update_rolled_back"), windows.MB_OK|windows.MB_ICONWARNING)
}

// confirmUpdate tells the old process this version works, then removes what
// the update left behind, once that process has exited.
func confirmUpdate(ctx context.Context, name, programDir string) {
	select {
	case <-time.After(updateHealthDelay):
	case <-ctx.Done():
		return
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return
	}
	event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, p)
	if err != nil {
		return
	}
	windows.SetEvent(event)
	windows.CloseHandle(event)
	time.Sleep(2 * time.Second)
	cleanLeftovers(programDir)
}
