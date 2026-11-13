package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mrgluek/passage/core/internal/sysdns"
	"github.com/mrgluek/passage/core/tunnel"
)

//go:embed licenses.txt
var licenses string

const (
	siteURL   = "https://passage.gluek.info"
	sourceURL = "https://github.com/mrgluek/passage"
	tickEvery = 5 * time.Second
)

// platform is what the app needs from the desktop it runs on.
type platform interface {
	copyText(text string) error
	open(target string) error // a URL or a folder
	canOpen(scheme string) bool
	autostart() bool
	setAutostart(on bool) error
	device() map[string]any // for the report
	updateTarget() string
	applyUpdate(archive, version string) error
}

// App runs the proxy from passage.ini and link.txt in dir, and
// answers the window's calls.
type App struct {
	dir     string
	version string
	iroh    string
	t       tr
	lang    string
	// systemLang is the language the system asked for; lang may differ by choice.
	systemLang string
	os         platform
	changed    func() // the tray redraws

	channels               channelDisplay
	mu                     sync.Mutex
	settings               Settings
	tun                    *tunnel.Tunnel
	lastErr                string
	linkStamp              fileStamp
	linkCfg                tunnel.Config
	linkErr                string
	results                []tunnel.ImportResult
	network                networkChange
	checking               time.Time
	log                    *appLog
	updater                desktopUpdate
	updateTransport        updateTransport
	runningSince           time.Time
	subscriptionEpoch      uint64
	subscriptionRefreshing bool
	subscriptionResults    map[string]string
	subscriptionNotice     string
	subscriptionFetcher    subscriptionFetcher
}

func newApp(dir, version, iroh, lang string, os platform) *App {
	a := &App{dir: dir, version: version, iroh: iroh, lang: lang, systemLang: lang, t: table(lang), os: os, changed: func() {}}
	a.log = openLog(a.path(logName), version)
	a.event("app start")
	s, existed, err := loadSettings(a.path(iniName))
	a.settings = s
	if s.Lang != "" {
		a.lang, a.t = s.Lang, table(s.Lang)
	}
	if err != nil {
		a.event("settings: %v", err)
	}
	if !existed {
		a.saveSettings()
	}
	a.loadLink()
	return a
}

// downloadURLs are the pages where the apps in the setup tabs can be got.
var downloadURLs = []string{"https://download.delta.chat", "https://velta.cc/#download"}

func (a *App) localization() (string, tr) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lang, a.t
}

// setLanguage switches the window's language: "en", "ru", or "" for the system's.
func (a *App) setLanguage(lang string) error {
	if lang != "" && lang != "en" && lang != "ru" {
		return errors.New("unknown language")
	}
	a.mu.Lock()
	a.settings.Lang = lang
	if lang == "" {
		lang = a.systemLang
	}
	a.lang, a.t = lang, table(lang)
	a.saveSettings()
	a.mu.Unlock()
	a.changed()
	return nil
}

func (a *App) path(name string) string { return filepath.Join(a.dir, name) }

// event adds a line to passage.log, shown under the router's events.
func (a *App) event(format string, args ...any) { a.log.add(format, args...) }

func (a *App) saveSettings() {
	if err := a.settings.save(a.path(iniName)); err != nil {
		a.event("settings not saved: %v", err)
	}
}

// loadLink reads link.txt; a bad line leaves the proxy without
// a tunnel until the file is fixed.
func (a *App) loadLink() {
	a.subscriptionEpoch++
	path := linkFile(a.dir)
	a.linkStamp = stampOf(path)
	a.linkCfg, a.results, a.linkErr = tunnel.Config{}, nil, ""
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		err = nil
		b = nil
	}
	if err == nil {
		a.linkCfg, a.results, err = importLinks(string(b))
	}
	if err == nil {
		if e := a.restoreSubscriptions(); e != nil {
			a.event("subscription state not loaded")
		}
	}
	a.linkCfg.RouteEdits = tunnel.MinimalEdits(a.linkCfg.Routes, a.settings.Edits)
	if err != nil {
		a.linkErr = err.Error()
		a.event("link file: %v", err)
	}
}

func (a *App) hasLink() bool {
	return len(a.linkCfg.Routes) > 0 || a.linkCfg.Zone != "" || len(a.linkCfg.Subscriptions) > 0
}

// routes is the installed routes that can be chosen.
func (a *App) routes() []routeChoice { return installedRoutes(a.linkCfg) }

// available reports whether mode can be chosen now.
func (a *App) available(mode string) bool {
	switch mode {
	case tunnel.ModeAuto, tunnel.ModeOff:
		return true
	case "my":
		return a.settings.My != ""
	}
	return slices.ContainsFunc(a.routes(), func(r routeChoice) bool { return r.Mode == mode })
}

// mode is the saved choice, Auto when the route is gone. An older choice of
// a route type reads as the first route of that type.
func (a *App) mode() string {
	m := a.settings.Mode
	if a.available(m) {
		return m
	}
	routes := a.routes()
	if m == tunnel.ModeAlways {
		if a.settings.My != "" && (len(routes) == 0 || myPriority <= routes[0].Priority) {
			return "my"
		}
		if len(routes) > 0 {
			return routes[0].Mode
		}
	}
	if i := slices.IndexFunc(routes, func(r routeChoice) bool { return r.Type == m }); i >= 0 {
		return routes[i].Mode
	}
	return tunnel.ModeAuto
}

// startLocked (re)starts the router with the current network's resolvers.
func (a *App) startLocked(why string) {
	if a.tun != nil {
		a.tun.Close()
		a.log.copyStalls(a.tun.Status(500).Events)
		a.tun = nil
	}
	cfg := a.linkCfg
	s := a.settings
	cfg.Listen = s.listen()
	if s.Auth {
		cfg.User, cfg.Pass = s.User, s.Pass
	}
	cfg.Mode = a.mode()
	cfg.My = s.My
	state, _ := readNetwork()
	cfg.SystemResolvers = state.dns
	cfg.IrohBinary = a.iroh
	data := a.path(dataName)
	cfg.PrivateDir = filepath.Join(data, "iroh")
	cfg.StateFile = filepath.Join(data, "router-state.json")
	os.MkdirAll(data, 0700)
	a.network = networkChange{current: state.key()}
	a.event("router start: %s mode=%s", why, cfg.Mode)
	cfg, err := cfg.Normalize()
	if err == nil {
		a.tun, err = tunnel.Start(cfg)
	}
	if err != nil {
		a.lastErr = fmt.Sprintf("%s failed: %v", why, err)
		a.event("%s", a.lastErr)
		return
	}
	a.lastErr = ""
}

// tick restarts a dead router, follows network and link file changes.
func (a *App) tick() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tun != nil {
		a.log.copyStalls(a.tun.Status(500).Events)
	}
	reload := stampOf(linkFile(a.dir)) != a.linkStamp
	if reload {
		a.loadLink()
		a.event("link file changed")
	}
	if !a.settings.Running {
		return
	}
	switch {
	case a.tun == nil:
		a.startLocked("retry")
	case reload:
		a.startLocked("restart on link change")
	case a.networkChanged(time.Now()):
		a.startLocked("restart on network change")
	default:
		if a.subscriptionRefreshing {
			return
		}
		select {
		case update := <-a.tun.SubscriptionUpdates():
			if err := a.applySubscriptionRefreshLocked(update.Config, update.Changed); err != nil {
				a.event("subscription state not saved")
				a.subscriptionNotice = err.Error()
				return
			}

		default:
			return
		}
	}
}

func (a *App) run(ctx context.Context) {
	a.mu.Lock()
	if a.settings.Running {
		a.runningSince = time.Now()
		a.startLocked("app start")
	}
	a.mu.Unlock()
	a.changed()
	tk := time.NewTicker(tickEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			a.cancelUpdate()
			a.event("app quit")
			a.mu.Lock()
			if a.tun != nil {
				a.tun.Close()
				a.log.copyStalls(a.tun.Status(500).Events)
				a.tun = nil
			}
			a.mu.Unlock()
			return
		case <-tk.C:
			a.tick()
			a.updateTick(time.Now())
			go a.changed()
		}
	}
}

func (a *App) setRunning(on bool) {
	a.mu.Lock()
	if on && !a.settings.Running {
		a.runningSince = time.Now()
	}
	if !on {
		a.runningSince = time.Time{}
		a.channels = channelDisplay{}
	}
	a.settings.Running = on
	a.saveSettings()
	if on {
		a.startLocked("user start")
	} else {
		a.event("user stop")
		if a.tun != nil {
			a.tun.Close()
			a.log.copyStalls(a.tun.Status(500).Events)
			a.tun = nil
		}
		a.lastErr = ""
	}
	a.mu.Unlock()
	a.changed()
}

func (a *App) restartIfRunningLocked(why string) {
	if a.settings.Running {
		a.startLocked(why)
	}
}

func (a *App) setMode(mode string) error {
	a.mu.Lock()
	defer a.changedAfter()
	defer a.mu.Unlock()
	if !a.available(mode) {
		return fmt.Errorf("unknown route %q", mode)
	}
	a.settings.Mode = mode
	a.saveSettings()
	if a.tun != nil {
		return a.tun.SetMode(mode)
	}
	return nil
}

func (a *App) changedAfter() { go a.changed() }

// trayState is what the tray shows.
type trayState struct {
	Lang    string
	Text    tr
	Running bool
	Tooltip string
	Mode    string
	Routes  []routeChoice
	My      bool
	Update  string
}

func (a *App) trayState() trayState {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := trayState{Lang: a.lang, Text: a.t, Running: a.settings.Running, Mode: a.mode(), Routes: a.routes(), My: a.settings.My != ""}
	st.Tooltip = "Passage: " + a.t.f(a.trayTitleKey())
	if a.tun != nil {
		s := a.tun.Status(0)
		if detail := statusDetail(a.t, s.Health, a.channels.sample(a.t, s, time.Now())); detail != "" {
			st.Tooltip += "\n" + detail
		}
	}
	if a.updater.offer != nil {
		st.Update = a.updater.offer.Version
	}
	return st
}

func (a *App) trayTitleKey() string {
	switch {
	case a.lastErr != "":
		return "h_title_error"
	case !a.hasLink():
		return "h_title_nolink"
	case !a.settings.Running:
		return "h_title_off"
	case a.tun == nil:
		return "h_title_connecting"
	}
	return healthTitleKey(a.tun.Status(0).Health)
}

func healthTitleKey(health tunnel.Health) string {
	switch health.State {
	case "starting":
		return "h_title_connecting"
	case "problem":
		switch health.Reason {
		case "no_internet", "server_down", "no_route", "slow":
			return "h_title_" + health.Reason
		default:
			return "h_title_slow"
		}
	case "working", "limited":
		return "h_title_working"
	default:
		return "h_title_on"
	}
}

// view is the home screen's state.
type view struct {
	SubscriptionsEmpty string            `json:"subscriptions_empty"`
	Subscriptions      []subscriptionRow `json:"subscriptions"`
	SubscriptionNotice string            `json:"subscription_notice"`
	Update             updateView        `json:"update"`
	Running            bool              `json:"running"`
	Title              string            `json:"title"`
	Channels           string            `json:"channels"`
	Head               string            `json:"head"`
	Line               string            `json:"line"`
	Full               string            `json:"full"`
	StartHint          string            `json:"start_hint"`
	HasLink            bool              `json:"has_link"`
	Mode               string            `json:"mode"`
	Routes             []routeChoice     `json:"routes"`
	My                 bool              `json:"my"`
	Auth               bool              `json:"auth"`
	Host               string            `json:"host"`
	Port               int               `json:"port"`
	User               string            `json:"user"`
	Pass               string            `json:"pass"`
	ProxyLink          string            `json:"proxy_link"` // socks5://
	TgLink             string            `json:"tg_link"`    // tg://socks
	VeltaLink          string            `json:"velta_link"` // velta://proxy/2
	// Traffic grows while the proxy carries bytes; the window pulses on it.
	Traffic int64 `json:"traffic"`
	// AppSeen is true once a chat app has used the proxy.
	AppSeen bool   `json:"app_seen"`
	Lang    string `json:"lang"`
	// Error is why the proxy could not start, if it could not.
	Error string `json:"error"`
	// Health is the router's plain verdict, empty while the proxy is stopped.
	Health tunnel.Health `json:"health"`
}

// startHint asks to start the proxy when routes exist but it is not running.
func startHint(t tr, running bool, routes int) string {
	if !running && routes > 0 {
		return t.f("start_hint")
	}
	return ""
}

func (a *App) view() view {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.settings
	v := view{Running: s.Running, HasLink: a.hasLink(), Mode: a.mode(), Routes: a.routes(),
		My: s.My != "", Auth: s.Auth, Host: host, Port: s.Port, ProxyLink: s.socksURL(), TgLink: s.proxyLink("tg://socks"), VeltaLink: s.veltaLink(),
		AppSeen: s.AppSeen, Lang: s.Lang, Error: a.lastErr}
	if s.Auth {
		v.User, v.Pass = s.User, s.Pass
	}
	switch {
	case s.Running && time.Now().Before(a.checking):
		v.Head = a.t.f("checking")
	case s.Running:
		v.Head = a.t.f("proxy_running")
	default:
		v.Head = a.t.f("proxy_stopped")
	}
	v.Title = "Passage: " + a.t.f(a.trayTitleKey())
	v.StartHint = startHint(a.t, v.Running, len(v.Routes))
	var full string
	if a.tun != nil {
		st := a.tun.Status(500)
		a.log.copyStalls(st.Events)
		if len(st.Events) > 30 {
			st.Events = st.Events[len(st.Events)-30:]
		}
		v.Line = oneLine(a.t, st)
		v.Channels = statusDetail(a.t, st.Health, a.channels.sample(a.t, st, time.Now()))
		v.Health = st.Health
		full = fullStatus(a.t, st)
		var conns int64
		for _, r := range st.Stats {
			conns += r.Conns
			v.Traffic += r.Up + r.Down
		}
		if conns > 0 && !a.settings.AppSeen {
			a.settings.AppSeen, v.AppSeen = true, true
			a.saveSettings()
		}
	} else {
		full = a.t.f("proxy_not_running") + "\n"
	}
	if a.lastErr != "" {
		full = "⚠ " + a.lastErr + "\n\n" + full
	}
	if a.linkErr != "" {
		full = "⚠ " + a.t.f("link_error", a.linkErr) + "\n\n" + full
	}
	if events := a.log.text(32); events != "" {
		full += "\n" + a.t.f("app_events") + "\n" + events
	}
	v.Subscriptions = a.subscriptionRowsLocked()
	if len(v.Subscriptions) == 0 {
		v.SubscriptionsEmpty = a.t.f("subscriptions_empty")
	}
	v.SubscriptionNotice = noticeText(a.t, a.settings.RoutesNotice)
	if a.subscriptionNotice != "" {
		v.SubscriptionNotice = a.subscriptionNotice
	}
	v.Update = a.updateViewLocked()
	v.Full = full
	return v
}

// linkView is the settings screen's link part. The text is sent only when
// asked, so the key isn't on screen by default.
type linkView struct {
	RoutesEmpty string `json:"routes_empty"`
	Has         bool   `json:"has"`
	Info        string `json:"info"`
	Error       string `json:"error"`
	Text        string `json:"text,omitempty"`
	// Routes are the link's routes with the local edits, for the editor.
	Routes []savedRoute `json:"routes"`
}

// savedRoute is one installed route as the editor shows it.
type savedRoute struct {
	Subscription string `json:"subscription,omitempty"`
	ID           string `json:"id"`
	Type         string `json:"type"`
	Priority     int    `json:"priority"`
	On           bool   `json:"on"`
}

func (a *App) linkView(withText bool) linkView {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := linkView{Has: a.hasLink()}
	v.Routes = savedRoutes(a.linkCfg)
	if len(v.Routes) == 0 {
		v.RoutesEmpty = a.t.f("saved_routes_empty")
	}
	switch {
	case a.linkErr != "":
		v.Error = a.t.f("link_invalid", a.linkErr)
	case v.Has:
		v.Info = describeLink(a.t, a.linkCfg)
	default:
		v.Info = a.t.f("link_empty")
	}
	if withText || a.linkErr != "" {
		b, _ := os.ReadFile(linkFile(a.dir))
		v.Text = string(b)
	}
	return v
}

// combineLink is link.txt after adding the pasted links: a link in
// merge mode is appended, one in replace mode starts the file over. An
// unreadable file counts as empty, so pasting a link repairs it.
func (a *App) combineLink(incoming string) (text string, replaced bool, err error) {
	var lines []string
	if b, rerr := os.ReadFile(linkFile(a.dir)); rerr == nil {
		if _, _, perr := importLinks(string(b)); perr == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if l = strings.TrimSpace(strings.TrimPrefix(l, "\ufeff")); l != "" {
					lines = append(lines, l)
				}
			}
		} else {
			replaced = true
		}
	}
	for _, line := range strings.Split(incoming, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r, ierr := tunnel.ImportLink(tunnel.Config{}, line, true)
		if ierr != nil {
			return "", false, ierr
		}
		if r.Mode == "replace" {
			lines, replaced = nil, true
		}
		if !slices.Contains(lines, line) {
			lines = append(lines, line)
		}
	}
	text = strings.Join(lines, "\n")
	if _, _, err = importLinks(text); err != nil {
		return "", false, err
	}
	return text, replaced, nil
}

// importPreview says what adding the pasted links would do, for the
// confirmation.
func (a *App) importPreview(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return a.t.f("import_clear"), nil
	}
	combined, replaced, err := a.combineLink(text)
	if err != nil {
		return "", err
	}
	_, results, _ := importLinks(combined)
	trusted := true
	var ids []string
	collision := false
	for _, r := range results {
		trusted = trusted && r.Signature == "trusted_author"
		ids = append(ids, r.IDs...)
		collision = collision || r.Collision
	}
	parts := []string{a.t.f("import_untrusted"), a.t.f("import_merge")}
	if replaced {
		parts[1] = a.t.f("import_replace")
	}
	if trusted {
		parts[0] = a.t.f("import_trusted")
	}
	if collision {
		parts[1] += "\n" + a.t.f("import_collision")
	}
	parts[1] += "\n" + strings.Join(ids, ", ")
	parts = append(parts, a.t.f("import_personal"))
	return strings.Join(parts, "\n\n"), nil
}

// saveLink adds the pasted links to link.txt; empty text removes the file.
func (a *App) saveLink(text string) error {
	incoming := text
	a.mu.Lock()
	checkErr := a.checkRouteLinks(text)
	a.mu.Unlock()
	if checkErr != nil {
		return checkErr
	}
	replaced := strings.TrimSpace(text) == ""
	if !replaced {
		combined, r, err := a.combineLink(text)
		if err != nil {
			return err
		}
		text, replaced = combined, r
	}
	text = strings.TrimSpace(text)
	a.mu.Lock()
	defer a.changedAfter()
	defer a.mu.Unlock()
	pending := a.linkCfg
	for _, line := range strings.Split(incoming, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r, err := tunnel.ImportLink(pending, line, true)
		if err != nil {
			return err
		}
		pending = r.Config
	}
	if strings.TrimSpace(incoming) == "" {
		pending = tunnel.Config{}
	}
	persistSubscriptions := len(a.linkCfg.Subscriptions) > 0
	if _, err := os.Stat(a.path("subscriptions.json")); err == nil {
		persistSubscriptions = true
	}
	path := a.path(linkName)
	// Saved here, it replaces a link.txt.txt for good.
	if err := os.Remove(path + ".txt"); err != nil && !os.IsNotExist(err) {
		return err
	}
	var err error
	if text == "" {
		err = os.Remove(path)
		if os.IsNotExist(err) {
			err = nil
		}
	} else {
		err = writeFile(path, []byte(text+"\n"))
	}
	if err != nil {
		return err
	}
	if persistSubscriptions {
		if err := tunnel.SaveSubscriptionState(a.path("subscriptions.json"), pending); err != nil {
			return errors.New(a.t.f("sub_save_failed"))
		}
	}
	if replaced && a.settings.Edits != nil {
		a.settings.Edits = nil
		a.saveSettings()
	}
	a.loadLink()
	a.event("link saved")
	a.restartIfRunningLocked("settings changed")
	return nil
}

// saveRouteEdits stores the editor's routes: only what differs from the
// link is kept.
func (a *App) saveRouteEdits(routes []savedRoute) error {
	a.mu.Lock()
	defer a.changedAfter()
	defer a.mu.Unlock()
	edits := map[string]tunnel.RouteEdit{}
	for _, r := range routes {
		if r.Priority < 0 || r.Priority > 65535 {
			return errors.New("priority must be 0-65535")
		}
		p := uint16(r.Priority)
		edits[r.ID] = tunnel.RouteEdit{Priority: &p, Off: !r.On}
	}
	edits = tunnel.MinimalEdits(a.linkCfg.Routes, edits)
	if maps.EqualFunc(edits, a.linkCfg.RouteEdits, routeEditEqual) {
		return nil
	}
	a.settings.Edits = edits
	a.saveSettings()
	a.linkCfg.RouteEdits = edits
	a.subscriptionEpoch++
	if a.settings.Mode != tunnel.ModeAuto && !a.available(a.settings.Mode) {
		a.settings.Mode = tunnel.ModeAuto
		a.saveSettings()
	}
	a.event("routes edited")
	a.restartIfRunningLocked("settings changed")
	return nil
}

func routeEditEqual(a, b tunnel.RouteEdit) bool {
	return a.Off == b.Off && (a.Priority == nil) == (b.Priority == nil) && (a.Priority == nil || *a.Priority == *b.Priority)
}

// saveMy stores My route in its short form; empty removes it.
func (a *App) saveMy(text string) (string, error) {
	value := ""
	if text = strings.TrimSpace(text); text != "" {
		cfg, err := tunnel.Config{My: text}.Normalize()
		if err != nil {
			return "", errors.New(a.t.f("my_route_invalid"))
		}
		value = cfg.My
	}
	a.mu.Lock()
	defer a.changedAfter()
	defer a.mu.Unlock()
	if value == a.settings.My {
		return value, nil
	}
	a.settings.My = value
	a.saveSettings()
	a.restartIfRunningLocked("settings changed")
	return value, nil
}

func (a *App) setAuth(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.Auth = on
	a.saveSettings()
	a.restartIfRunningLocked("settings changed")
}

func (a *App) newAccess() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.newAccess()
	a.saveSettings()
	a.restartIfRunningLocked("settings changed")
}

func (a *App) recheck() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tun != nil {
		a.tun.Recheck()
		a.checking = time.Now().Add(3 * time.Second)
	}
}

// report is the status for a bug report, with a network test when the
// proxy runs. It leaves out the tunnel zone, like everything the link carries.
func (a *App) report() string {
	a.mu.Lock()
	t := a.tun
	text := a.t
	a.mu.Unlock()
	var test []string
	if t != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		d := t.Diagnose(ctx)
		cancel()
		a.mu.Lock()
		a.event("network test: %s", diagnosisSummary(d))
		a.mu.Unlock()
		test = strings.Split(strings.TrimRight(diagnosis(text, d), "\n"), "\n")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r := map[string]any{"running": a.tun != nil}
	if a.tun != nil {
		var st map[string]any
		status := a.tun.Status(500)
		a.log.copyStalls(status.Events)
		if len(status.Events) > 100 {
			status.Events = status.Events[len(status.Events)-100:]
		}
		b, _ := json.Marshal(status)
		json.Unmarshal(b, &st)
		delete(st, "zone")
		for k, v := range st {
			r[k] = v
		}
	}
	r["app"] = map[string]any{
		"version":      a.version,
		"platform":     runtime.GOOS + "/" + runtime.GOARCH,
		"listen":       a.settings.listen(),
		"auth":         a.settings.Auth,
		"mode_setting": a.settings.Mode,
		"last_error":   redact(a.lastErr),
		"link_error":   redact(a.linkErr),
	}
	r["device"] = a.os.device()
	history := a.log.last(logKeep)
	for i, line := range history {
		history[i] = redact(line)
	}
	r["diagnostics"] = map[string]any{"events": history}
	r["device_network"] = map[string]any{"dns": sysdns.Resolvers()}
	if test != nil {
		r["network_test"] = test
	}
	maskReportAddresses(r, reportPrefixes())
	b, _ := json.MarshalIndent(r, "", "  ")
	return string(b)
}

// call is the window's API: one method name and its JSON argument.
func (a *App) call(method string, arg json.RawMessage) (any, error) {
	str := func() string {
		var s string
		json.Unmarshal(arg, &s)
		return s
	}
	flag := func() bool {
		var b bool
		json.Unmarshal(arg, &b)
		return b
	}
	switch method {
	case "init":
		lang, text := a.localization()
		return map[string]any{"lang": lang, "strings": text, "version": a.version, "dir": a.dir,
			"site": siteURL, "source": sourceURL}, nil
	case "view":
		return a.view(), nil
	case "start":
		a.setRunning(true)
	case "stop":
		a.setRunning(false)
	case "setMode":
		return nil, a.setMode(str())
	case "recheck":
		a.recheck()
	case "link":
		return a.linkView(flag()), nil
	case "importPreview":
		return a.previewImport(str())
	case "importLink":
		var in struct {
			Link      string `json:"link"`
			Replace   bool   `json:"replace"`
			Subscribe bool   `json:"subscribe"`
		}
		if err := json.Unmarshal(arg, &in); err != nil {
			return nil, err
		}
		if in.Subscribe {
			return nil, a.subscribe(in.Link, in.Replace)
		}
		return nil, a.saveLink(in.Link)
	case "dismissSubscriptionNotice":
		a.mu.Lock()
		a.settings.RoutesNotice = ""
		a.subscriptionNotice = ""
		a.saveSettings()
		a.mu.Unlock()
		a.changed()
		return nil, nil
	case "refreshSubscriptions":
		return nil, a.refreshSubscriptions(str())
	case "removeSubscription":
		return nil, a.removeSubscription(str())
	case "saveRoutes":
		var routes []savedRoute
		if err := json.Unmarshal(arg, &routes); err != nil {
			return nil, err
		}
		return nil, a.saveRouteEdits(routes)
	case "saveLink":
		return nil, a.saveLink(str())
	case "updateCheck":
		return nil, a.checkUpdate()
	case "updateDownload":
		return nil, a.downloadUpdate()
	case "updateCancel":
		a.cancelUpdate()
	case "updateInstall":
		return nil, a.installUpdate()
	case "setAutoUpdate":
		a.mu.Lock()
		a.settings.AutoUpdate = flag()
		a.saveSettings()
		a.mu.Unlock()
	case "settings":
		a.mu.Lock()
		defer a.mu.Unlock()
		return map[string]any{"auth": a.settings.Auth, "my": a.settings.My, "autostart": a.os.autostart(),
			"auto_update": a.settings.AutoUpdate, "soar": tunnel.SoarVersion()}, nil
	case "saveMy":
		return a.saveMy(str())
	case "setAuth":
		a.setAuth(flag())
	case "newAccess":
		a.newAccess()
	case "setAutostart":
		return nil, a.os.setAutostart(flag())
	case "copy":
		return nil, a.os.copyText(str())
	case "open":
		target := str()
		if target != siteURL && target != sourceURL && !slices.Contains(downloadURLs, target) {
			return nil, errors.New("not an allowed link")
		}
		return nil, a.os.open(target)
	case "openFolder":
		return nil, a.os.open(a.dir)
	case "openVelta":
		if !a.os.canOpen("velta") {
			return false, nil
		}
		a.mu.Lock()
		link := a.settings.veltaLink()
		a.mu.Unlock()
		return true, a.os.open(link)
	case "setLanguage":
		return nil, a.setLanguage(str())
	case "openTg":
		if !a.os.canOpen("tg") {
			return false, nil
		}
		a.mu.Lock()
		link := a.settings.proxyLink("tg://socks")
		a.mu.Unlock()
		return true, a.os.open(link)
	case "report":
		text := a.report()
		return nil, a.os.copyText(text)
	case "licenses":
		return licenses, nil
	default:
		return nil, fmt.Errorf("unknown call %q", method)
	}
	return nil, nil
}
