package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	exeSuffix = ".exe"
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue  = "Passage"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	openClipboard    = user32.NewProc("OpenClipboard")
	closeClipboard   = user32.NewProc("CloseClipboard")
	emptyClipboard   = user32.NewProc("EmptyClipboard")
	setClipboardData = user32.NewProc("SetClipboardData")
	getDpiForSystem  = user32.NewProc("GetDpiForSystem")
	setForeground    = user32.NewProc("SetForegroundWindow")
	showWindow       = user32.NewProc("ShowWindow")
	isIconic         = user32.NewProc("IsIconic")
	allowForeground  = user32.NewProc("AllowSetForegroundWindow")
	globalAlloc      = kernel32.NewProc("GlobalAlloc")
	globalFree       = kernel32.NewProc("GlobalFree")
	globalLock       = kernel32.NewProc("GlobalLock")
	globalUnlock     = kernel32.NewProc("GlobalUnlock")
	moveMemory       = kernel32.NewProc("RtlMoveMemory")
)

type winPlatform struct{}

func (winPlatform) copyText(text string) error {
	u, err := windows.UTF16FromString(strings.ReplaceAll(text, "\n", "\r\n"))
	if err != nil {
		return err
	}
	if r, _, err := openClipboard.Call(0); r == 0 {
		return err
	}
	defer closeClipboard.Call()
	emptyClipboard.Call()
	size := uintptr(len(u) * 2)
	h, _, err := globalAlloc.Call(0x0002, size) // GMEM_MOVEABLE
	if h == 0 {
		return err
	}
	p, _, err := globalLock.Call(h)
	if p == 0 {
		globalFree.Call(h)
		return err
	}
	moveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	globalUnlock.Call(h)
	if r, _, err := setClipboardData.Call(13, h); r == 0 { // CF_UNICODETEXT
		globalFree.Call(h)
		return err
	}
	return nil
}

func (winPlatform) open(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// canOpen reports whether an app is registered for the scheme. Asking the
// shell to open it without one would offer the Microsoft Store.
func (winPlatform) canOpen(scheme string) bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, scheme+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// startCommand is how sign-in starts this copy of Passage.
func startCommand() string {
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return `"` + exe + `" -autostart`
}

// autostart reports whether sign-in starts this copy: a moved folder
// leaves the old entry, which then shows as off.
func (winPlatform) autostart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	return err == nil && strings.EqualFold(v, startCommand())
}

func (winPlatform) setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(runValue, startCommand())
	}
	if err = k.DeleteValue(runValue); errors.Is(err, registry.ErrNotExist) {
		err = nil
	}
	return err
}

func (winPlatform) device() map[string]any {
	v := windows.RtlGetVersion()
	webview, _ := webviewloader.GetInstalledVersion()
	return map[string]any{
		"os":      "windows",
		"version": fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber),
		"arch":    runtime.GOARCH,
		"go":      runtime.Version(),
		"webview": webview,
	}
}

// uiLanguage is the user's Windows display language, such as ru-RU.
func uiLanguage() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(langs) == 0 {
		return ""
	}
	return langs[0]
}

// dpiScale is the display scale; the window size is in pixels.
func dpiScale() float64 {
	if getDpiForSystem.Find() != nil {
		return 1
	}
	dpi, _, _ := getDpiForSystem.Call()
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// bringToFront shows a hidden or minimized window above the others.
func bringToFront(hwnd uintptr) {
	if r, _, _ := isIconic.Call(hwnd); r != 0 {
		showWindow.Call(hwnd, 9) // SW_RESTORE
	}
	setForeground.Call(hwnd)
}

func messageBox(text string, flags uint32) int32 {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Passage")
	r, _ := windows.MessageBox(0, t, c, flags)
	return r
}

func showError(text string) { messageBox(text, windows.MB_OK|windows.MB_ICONERROR) }

// firstInstance claims the per-user "open the window" event. A second
// start signals it, so the running Passage opens its window, and quits.
func firstInstance() (windows.Handle, bool) {
	name, _ := windows.UTF16PtrFromString(`Local\PassageOpenWindow`)
	h, err := windows.CreateEvent(nil, 0, 0, name)
	if h == 0 {
		return 0, true // no event: run anyway
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		allowForeground.Call(0xFFFFFFFF) // ASFW_ANY: the first may raise its window
		windows.SetEvent(h)
		windows.CloseHandle(h)
		return 0, false
	}
	return h, true
}
