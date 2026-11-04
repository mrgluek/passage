package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"fyne.io/systray"
	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/mrgluek/passage/core/tunnel"
)

var (
	//go:embed icons/tray-on.ico
	iconOn []byte
	//go:embed icons/tray-off.ico
	iconOff []byte
)

const webviewDownload = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

func runDesktop(dir string, autostart bool) {
	t := tr(table(language(uiLanguage())))
	if insideArchive(dir, os.TempDir()) {
		messageBox(t.f("inside_archive"), windows.MB_OK|windows.MB_ICONWARNING)
		return
	}
	event, first := firstInstance()
	if !first {
		return
	}
	a := newApp(dir, version, irohHelper(), language(uiLanguage()), winPlatform{})
	d := &desktop{app: a, dir: dir}
	a.changed = d.update
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	systray.Run(func() {
		systray.SetOnTapped(d.openWindow)
		d.update()
		go func() { a.run(ctx); close(done) }()
		if event != 0 {
			go func() {
				for {
					if _, err := windows.WaitForSingleObject(event, windows.INFINITE); err != nil {
						return
					}
					d.openWindow()
				}
			}()
		}
		if !autostart {
			d.openWindow()
		}
	}, func() {
		cancel()
		<-done
	})
}

// desktop is the tray icon with its menu, and the window.
type desktop struct {
	app *App
	dir string

	mu      sync.Mutex
	shown   *trayState // what the menu was built for
	toggle  *systray.MenuItem
	routes  map[string]*systray.MenuItem
	view    webview2.WebView
	opening bool
}

// update redraws the icon and tooltip, and rebuilds the menu when the
// route choices changed.
func (d *desktop) update() {
	st := d.app.trayState()
	d.mu.Lock()
	defer d.mu.Unlock()
	if st.Running {
		systray.SetIcon(iconOn)
	} else {
		systray.SetIcon(iconOff)
	}
	systray.SetTooltip(st.Tooltip)
	if d.shown == nil || d.shown.My != st.My || !slices.Equal(d.shown.Routes, st.Routes) {
		d.buildMenu(st)
	}
	t := d.app.t
	if st.Running {
		d.toggle.SetTitle(t.f("action_stop"))
	} else {
		d.toggle.SetTitle(t.f("action_start"))
	}
	for mode, item := range d.routes {
		if mode == st.Mode {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	d.shown = &st
}

func (d *desktop) buildMenu(st trayState) {
	t := d.app.t
	systray.ResetMenu()
	onClick(systray.AddMenuItem(t.f("tray_open"), ""), d.openWindow)
	systray.AddSeparator()
	d.toggle = systray.AddMenuItem(t.f("action_start"), "")
	onClick(d.toggle, func() { d.app.setRunning(!d.app.trayState().Running) })
	route := systray.AddMenuItem(t.f("route_label"), "")
	d.routes = map[string]*systray.MenuItem{}
	add := func(mode, label string) {
		item := route.AddSubMenuItemCheckbox(label, "", false)
		d.routes[mode] = item
		onClick(item, func() { d.app.setMode(mode) })
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
	onClick(systray.AddMenuItem(t.f("tray_quit"), ""), systray.Quit)
}

// onClick runs f for each click until the item is removed.
func onClick(item *systray.MenuItem, f func()) {
	go func() {
		for range item.ClickedCh {
			f()
		}
	}()
}

// openWindow shows the window, creating it on its own thread: closing it
// frees the browser, and the tray keeps running.
func (d *desktop) openWindow() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.view != nil {
		w := d.view
		w.Dispatch(func() { bringToFront(uintptr(w.Window())) })
		return
	}
	if d.opening {
		return
	}
	d.opening = true
	go d.runWindow()
}

func (d *desktop) runWindow() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	scale := dpiScale()
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  filepath.Join(d.dir, dataName, "webview2"),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Passage",
			Width:  uint(560 * scale),
			Height: uint(780 * scale),
			IconId: 1, // the program's icon (go-winres)
			Center: true,
		},
	})
	d.mu.Lock()
	d.opening = false
	d.view = w
	d.mu.Unlock()
	if w == nil {
		if messageBox(d.app.t.f("webview_missing"), windows.MB_YESNO|windows.MB_ICONWARNING) == 6 { // IDYES
			winPlatform{}.open(webviewDownload)
		}
		return
	}
	w.SetSize(int(400*scale), int(480*scale), webview2.HintMin)
	// Calls answer later: the window's thread must not wait for a network test.
	w.Bind("passageCall", func(id int, method string, arg json.RawMessage) {
		go func() {
			res, err := d.app.call(method, arg)
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			out, _ := json.Marshal(map[string]any{"result": res, "error": msg})
			w.Dispatch(func() { w.Eval("window.passageReply(" + jsonInt(id) + "," + string(out) + ")") })
		}()
	})
	w.SetHtml(indexHTML)
	w.Run()
	d.mu.Lock()
	d.view = nil
	d.mu.Unlock()
	w.Destroy()
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
