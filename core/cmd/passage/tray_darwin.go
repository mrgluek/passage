//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit -framework ServiceManagement
#include "mac_darwin.h"
*/
import "C"

import (
	bytebuf "bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"fyne.io/systray"

	"github.com/mrgluek/passage/core/tunnel"
)

const exeSuffix = ""

var (
	macApp  *App
	macQuit func()
	macStop func()
)

func runDesktop(dir string, autostart bool) {
	instance, err := claimMacInstance(dir)
	if err != nil {
		fatal(err)
	}
	if instance == nil {
		return
	}
	defer instance.close()
	C.passagePrepare()
	a := newApp(dir, version, irohHelper(), language(C.GoString(C.passageLanguage())), macPlatform{})
	macApp = a
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			<-done
		})
	}
	defer stop()
	macStop = func() {
		stop()
		instance.close()
	}
	macQuit = func() {
		stop()
		systray.Quit()
	}
	d := &macDesktop{app: a}
	a.changed = d.update
	systray.Run(func() {
		icon := macTrayIcon()
		systray.SetTemplateIcon(icon, icon)
		d.update()
		go func() { a.run(ctx); close(done) }()
		go func() {
			for {
				select {
				case <-instance.open:
					showMacWindow()
				case <-ctx.Done():
					return
				}
			}
		}()
		if !autostart && C.passageWasLoginLaunch() == 0 {
			showMacWindow()
		}
	}, stop)
}

type macDesktop struct {
	app      *App
	mu       sync.Mutex
	menuDone chan struct{}
	shown    *trayState
	toggle   *systray.MenuItem
	routes   map[string]*systray.MenuItem
}

func (d *macDesktop) update() {
	st := d.app.trayState()
	d.mu.Lock()
	defer d.mu.Unlock()
	systray.SetTooltip(st.Tooltip)
	if d.shown == nil || d.shown.My != st.My || d.shown.Update != st.Update || !slices.Equal(d.shown.Routes, st.Routes) {
		d.buildMenu(st)
	}
	key := "action_start"
	if st.Running {
		key = "action_stop"
	}
	d.toggle.SetTitle(d.app.t.f(key))
	for mode, item := range d.routes {
		if mode == st.Mode {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	d.shown = &st
}

func (d *macDesktop) buildMenu(st trayState) {
	t := d.app.t
	if d.menuDone != nil {
		close(d.menuDone)
	}
	d.menuDone = make(chan struct{})
	done := d.menuDone
	onMenuClick := func(item *systray.MenuItem, f func()) { go listenMenu(done, item.ClickedCh, f) }
	systray.ResetMenu()
	onMenuClick(systray.AddMenuItem(t.f("tray_open"), ""), showMacWindow)
	if st.Update != "" {
		onMenuClick(systray.AddMenuItem(t.f("update_available", st.Update), ""), showMacWindow)
	}
	systray.AddSeparator()
	d.toggle = systray.AddMenuItem(t.f("action_start"), "")
	onMenuClick(d.toggle, func() { d.app.setRunning(!d.app.trayState().Running) })
	route := systray.AddMenuItem(t.f("route_label"), "")
	d.routes = map[string]*systray.MenuItem{}
	add := func(mode, label string) {
		item := route.AddSubMenuItemCheckbox(label, "", false)
		d.routes[mode] = item
		onMenuClick(item, func() { d.app.setMode(mode) })
	}
	add(tunnel.ModeAuto, t.f("mode_auto"))
	if st.My {
		add("my", t.f("route_my"))
	}
	for _, r := range st.Routes {
		add(r.Mode, r.Label)
	}
	add(tunnel.ModeOff, t.f("mode_off"))
	systray.AddSeparator()
	onMenuClick(systray.AddMenuItem(t.f("tray_quit"), ""), macQuit)
}

func showMacWindow() {
	html := C.CString(indexHTML)
	defer C.free(unsafe.Pointer(html))
	C.passageShowWindow(html)
}

//export passageQuit
func passageQuit() { go macQuit() }

//export passageTerminate
func passageTerminate() {
	go func() {
		macStop()
		C.passageTerminationReady()
	}()
}

//export passageOpen
func passageOpen() { showMacWindow() }

//export passageCall
func passageCall(message *C.char) {
	var in struct {
		ID     int             `json:"id"`
		Method string          `json:"method"`
		Arg    json.RawMessage `json:"arg"`
	}
	if json.Unmarshal([]byte(C.GoString(message)), &in) != nil || in.ID <= 0 || macApp == nil {
		return
	}
	go func() {
		result, err := macApp.call(in.Method, in.Arg)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		out, err := json.Marshal(map[string]any{"result": result, "error": msg})
		if err != nil {
			out = []byte(`{"error":"Could not encode the response"}`)
		}
		script := C.CString("window.passageReply(" + strconv.Itoa(in.ID) + "," + string(out) + ")")
		defer C.free(unsafe.Pointer(script))
		C.passageReply(script)
	}()
}

func showError(message string) {
	text := C.CString(message)
	defer C.free(unsafe.Pointer(text))
	C.passageError(text)
}

type macPlatform struct{}

func (macPlatform) copyText(text string) error {
	cmd := exec.Command("/usr/bin/pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func (macPlatform) open(target string) error {
	return exec.Command("/usr/bin/open", target).Run()
}

func (macPlatform) canOpen(scheme string) bool {
	text := C.CString(scheme)
	defer C.free(unsafe.Pointer(text))
	return C.passageCanOpen(text) != 0
}

func (macPlatform) device() map[string]any {
	return map[string]any{"os": "darwin", "arch": runtime.GOARCH, "go": runtime.Version()}
}

func (macPlatform) autostart() bool { return C.passageAutostart() != 0 }
func (macPlatform) setAutostart(on bool) error {
	var enabled C.int
	if on {
		enabled = 1
	}
	if message := C.passageSetAutostart(enabled); message != nil {
		defer C.free(unsafe.Pointer(message))
		return errors.New(C.GoString(message))
	}
	return nil
}

// macTrayIcon draws the Passage arch as a Retina template image.
func macTrayIcon() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 36, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 36; x++ {
			xf, yf := float64(x)-17.5, float64(y)-17.5
			arch := yf < 0 && math.Abs(math.Hypot(xf, yf)-10) < 2
			legs := yf >= 0 && yf < 11 && math.Abs(math.Abs(xf)-10) < 2
			road := math.Abs(xf) < 1.5 && y >= 18 && y < 34 && (y-18)%6 < 4
			if arch || legs || road {
				img.SetNRGBA(x, y, color.NRGBA{A: 255})
			}
		}
	}
	var out bytebuf.Buffer
	png.Encode(&out, img)
	return out.Bytes()
}
