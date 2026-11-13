package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	links "github.com/mrgluek/passage-link/link"
	"github.com/mrgluek/passage/core/tunnel"
	"github.com/mrgluek/passage/core/update"
)

func TestTrayState(t *testing.T) {
	a := &App{t: table("en")}
	a.updater.offer = &update.Offer{Version: "1.2.3"}
	check := func(title string) {
		t.Helper()
		st := a.trayState()
		if strings.SplitN(st.Tooltip, "\n", 2)[0] != "Passage: "+title || st.Running != a.settings.Running || st.Update != "1.2.3" {
			t.Fatalf("tray state = %+v, want title %q", st, title)
		}
	}
	check("add your link")
	a.settings.Running = true
	check("add your link")
	a.lastErr = "start failed: test error"
	check("couldn't turn on")
	a.lastErr = ""
	a.linkCfg.Subscriptions = []tunnel.Subscription{{ID: "example"}}
	check("connecting…")
	router, err := tunnel.Start(tunnel.Config{Mode: tunnel.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Close)
	a.tun = router
	check("connecting…")
	a.settings.Running = false
	check("proxy is off")
	a.t = table("ru")
	check("прокси выключен")
}

func TestTrayHealthTitle(t *testing.T) {
	for _, tt := range []struct {
		state, reason, title string
	}{
		{"", "", "proxy is on"},
		{"ready", "waiting_app", "proxy is on"},
		{"starting", "", "connecting…"},
		{"working", "", "working"},
		{"limited", "reserve", "working"},
		{"problem", "no_internet", "no internet"},
		{"problem", "server_down", "chat server isn't answering"},
		{"problem", "no_route", "can't connect"},
		{"problem", "slow", "still connecting"},
		{"problem", "unknown", "still connecting"},
	} {
		t.Run(tt.state+"/"+tt.reason, func(t *testing.T) {
			h := tunnel.Health{State: tt.state, Reason: tt.reason}
			if got := tr(table("en")).f(healthTitleKey(h)); got != tt.title {
				t.Fatalf("title = %q, want %q", got, tt.title)
			}
		})
	}
}

func testLink(zone string) string {
	pub, _, _ := ed25519.GenerateKey(nil)
	return "passage://tunnel?v=1&zone=" + zone + "&key=" + base64.StdEncoding.EncodeToString(pub)
}

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), iniName)
	s, existed, err := loadSettings(path)
	if err != nil || existed {
		t.Fatalf("missing file: existed=%v err=%v", existed, err)
	}
	if s.Port < minPort || s.Port >= maxPort || len(s.Pass) != 32 || !s.Auth || !s.Running {
		t.Fatalf("defaults: %+v", s)
	}
	s.Mode, s.My, s.Auth, s.Running = "wss", "user:p%40ss@127.0.0.1:9050", false, false
	p20 := uint16(20)
	s.Edits = map[string]tunnel.RouteEdit{"cf": {Priority: &p20, Off: true}, "br": {Off: true}}
	if err := s.save(path); err != nil {
		t.Fatal(err)
	}
	got, existed, err := loadSettings(path)
	if err != nil || !existed || !reflect.DeepEqual(got, s) {
		t.Fatalf("got %+v, want %+v (err %v)", got, s, err)
	}
}

func TestSettingsHandEdited(t *testing.T) {
	path := filepath.Join(t.TempDir(), iniName)
	os.WriteFile(path, []byte("; comment\r\n[proxy]\r\nport = 23456\r\nauth=0\r\nunknown=1\r\n"), 0600)
	s, _, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	// No credentials in the file: new ones, the hand-set port stays.
	if s.Port != 23456 || s.Auth || s.User == "" || s.Mode != tunnel.ModeAuto {
		t.Fatalf("%+v", s)
	}
	os.WriteFile(path, []byte("port=23456\nuser=u\npass=p\n"), 0600)
	if s, _, _ = loadSettings(path); s.Port != 23456 || s.User != "u" || s.Pass != "p" {
		t.Fatalf("%+v", s)
	}
}

func TestProxyLink(t *testing.T) {
	s := Settings{Port: 20001, User: "u", Pass: "p", Auth: true}
	if got := s.proxyLink("tg://socks"); got != "tg://socks?server=127.0.0.1&port=20001&user=u&pass=p" {
		t.Fatal(got)
	}
	if got := s.socksURL(); got != "socks5://u:p@127.0.0.1:20001" {
		t.Fatal(got)
	}
	s.Auth = false
	if got := s.proxyLink("tg://socks"); got != "tg://socks?server=127.0.0.1&port=20001" {
		t.Fatal(got)
	}
	if got := s.socksURL(); got != "socks5://127.0.0.1:20001" {
		t.Fatal(got)
	}
}

func TestImportLinks(t *testing.T) {
	cfg, results, err := importLinks("\ufeff# my link\r\n\r\n" + testLink("t.example.org") + "\r\n")
	if err != nil || len(results) != 1 || cfg.Zone != "t.example.org" {
		t.Fatalf("cfg %+v results %d err %v", cfg, len(results), err)
	}
	if _, _, err = importLinks("# none\n"); err != nil {
		t.Fatal(err)
	}
	_, _, err = importLinks(testLink("t.example.org") + "\nnot a link\n")
	if err == nil || !strings.HasPrefix(err.Error(), "line 2:") {
		t.Fatalf("err %v", err)
	}
}

func TestInstalledRoutes(t *testing.T) {
	cfg := tunnel.Config{Routes: []links.Route{
		{ID: "b", Type: "iroh", Priority: 20},
		{ID: "a", Type: "wss", Priority: 10},
		{ID: "c", Type: "wss", Priority: 30},
		{ID: "wss", Type: "wss", Priority: 40},
		{ID: "z", Type: "future", Priority: 5},
	}}
	got := installedRoutes(cfg)
	var modes, labels []string
	for _, r := range got {
		modes, labels = append(modes, r.Mode), append(labels, r.Label)
	}
	if !slices.Equal(modes, []string{"route:a", "route:b", "route:c", "route:wss"}) {
		t.Fatal(modes)
	}
	if !slices.Equal(labels, []string{"10 a (wss)", "20 b (iroh)", "30 c (wss)", "40 wss"}) {
		t.Fatal(labels)
	}
	if r := installedRoutes(tunnel.Config{Zone: "t.example.org"}); len(r) != 1 || r[0].Mode != "route:legacy-dns" || r[0].Type != "soar" {
		t.Fatal(r)
	}
	if r := installedRoutes(tunnel.Config{}); r == nil || len(r) != 0 {
		t.Fatal(r)
	}
}

func TestModeMigratesToRoutes(t *testing.T) {
	a := &App{}
	a.linkCfg = tunnel.Config{Routes: []links.Route{
		{ID: "b", Type: "wss", Priority: 20},
		{ID: "a", Type: "iroh", Priority: 10},
		{ID: "c", Type: "wss", Priority: 30},
	}}
	for saved, want := range map[string]string{
		"auto": "auto", "off": "off", "route:c": "route:c",
		"wss": "route:b", "always": "route:a", "soar": "auto", "my": "auto", "route:gone": "auto",
	} {
		a.settings.Mode = saved
		if got := a.mode(); got != want {
			t.Fatalf("%s: got %s, want %s", saved, got, want)
		}
	}
	a.settings.My = "127.0.0.1:9050"
	for saved, want := range map[string]string{"my": "my", "always": "my"} {
		a.settings.Mode = saved
		if got := a.mode(); got != want {
			t.Fatalf("%s with My: got %s, want %s", saved, got, want)
		}
	}
}

func TestOnlyTypeFollowsChosenRoute(t *testing.T) {
	s := tunnel.Status{Mode: "route:b", Tunnels: []tunnel.RouteState{{ID: "a", Type: "iroh"}, {ID: "b", Type: "wss"}}}
	if got := onlyType(s); got != "wss" {
		t.Fatal(got)
	}
}

type fakePlatform struct{ copied string }

func (f *fakePlatform) copyText(s string) error { f.copied = s; return nil }
func (f *fakePlatform) open(string) error       { return nil }
func (f *fakePlatform) canOpen(string) bool     { return false }
func (f *fakePlatform) autostart() bool         { return false }
func (f *fakePlatform) setAutostart(bool) error { return nil }
func (f *fakePlatform) device() map[string]any  { return map[string]any{"os": "test"} }

func TestAppLinkFile(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir, "test", "", "en", &fakePlatform{})
	if _, err := os.Stat(filepath.Join(dir, iniName)); err != nil {
		t.Fatal("passage.ini not written:", err)
	}
	if a.hasLink() || a.linkView(false).Info != a.t.f("link_empty") {
		t.Fatal("link without a file")
	}
	if err := a.saveLink("bad"); err == nil {
		t.Fatal("bad link saved")
	}
	if err := a.saveLink(testLink("t.example.org")); err != nil {
		t.Fatal(err)
	}
	if routes := a.routes(); !a.hasLink() || len(routes) != 1 || routes[0].Type != "soar" {
		t.Fatalf("routes %v", routes)
	}
	// A file written by hand is picked up on the next tick.
	os.WriteFile(filepath.Join(dir, linkName), []byte("broken\n"), 0600)
	a.linkStamp = fileStamp{}
	a.settings.Running = false
	a.tick()
	if a.hasLink() || a.linkView(false).Error == "" {
		t.Fatal("broken link file not reported")
	}
	if err := a.saveLink(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, linkName)); !os.IsNotExist(err) {
		t.Fatal("empty link kept the file")
	}
}

func TestAppCalls(t *testing.T) {
	fake := &fakePlatform{}
	a := newApp(t.TempDir(), "test", "", "ru", fake)
	a.settings.Running = false
	out, err := a.call("init", nil)
	if err != nil || out.(map[string]any)["strings"].(tr)["save"] != "Сохранить" {
		t.Fatalf("init: %v %v", out, err)
	}
	if _, err := a.call("saveMy", json.RawMessage(`"not an address"`)); err == nil {
		t.Fatal("bad My route saved")
	}
	out, err = a.call("saveMy", json.RawMessage(`"socks5://127.0.0.1:9050"`))
	if err != nil || out != "127.0.0.1:9050" || a.settings.My != "127.0.0.1:9050" {
		t.Fatalf("saveMy: %v %v", out, err)
	}
	if _, err := a.call("setMode", json.RawMessage(`"my"`)); err != nil || a.mode() != "my" {
		t.Fatalf("setMode: %v %s", err, a.mode())
	}
	if _, err := a.call("open", json.RawMessage(`"https://example.org"`)); err == nil {
		t.Fatal("opened a link that is not ours")
	}
	if _, err := a.call("report", nil); err != nil || !strings.Contains(fake.copied, `"os": "test"`) ||
		!strings.Contains(fake.copied, "test app start") {
		t.Fatalf("report: %v\n%s", err, fake.copied)
	}
	v := a.view()
	if v.Running || v.Head != "Прокси остановлен" || !strings.Contains(v.Full, "Прокси не запущен") ||
		!strings.Contains(v.Full, "passage.log") {
		t.Fatalf("%+v", v)
	}
}

func TestStrings(t *testing.T) {
	for key := range texts["ru"] {
		englishKey := key
		for _, form := range []string{"_few", "_many"} {
			if strings.HasSuffix(key, form) {
				englishKey = strings.TrimSuffix(key, form) + "_other"
			}
		}
		if _, ok := texts["en"][englishKey]; !ok {
			t.Errorf("ru has %q, en does not", key)
		}
	}
	if got := tr(table("ru")).f("status_open_conns", 2, 3); got != "Открытые соединения: напрямую 2, через туннель 3" {
		t.Fatal(got)
	}
}

func TestAppLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), logName)
	l := openLog(path, "1.0")
	l.add("start failed: link passage://tunnel?v=1&zone=t.example.org&key=abc, pass=secret")
	got := l.last(1)[0]
	if strings.Contains(got, "example.org") || strings.Contains(got, "secret") || !strings.Contains(got, " 1.0 start failed: link [link]") {
		t.Fatal(got)
	}
	// Kept across restarts, rotated past logMax.
	for i := 0; i < 3000; i++ {
		l.add("event %d %s", i, strings.Repeat("x", 100))
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("not rotated:", err)
	}
	if fi, _ := os.Stat(path); fi.Size() > logMax+logMsgMax {
		t.Fatalf("log is %d bytes", fi.Size())
	}
	again := openLog(path, "1.0").last(logKeep)
	if len(again) != logKeep || !strings.Contains(again[len(again)-1], "event 2999 ") {
		t.Fatalf("%d lines, last %q", len(again), again[len(again)-1])
	}
}

func TestLinkFileFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, linkName+".txt"), []byte(testLink("t.example.org")+"\n"), 0600)
	a := newApp(dir, "test", "", "en", &fakePlatform{})
	a.settings.Running = false
	if !a.hasLink() {
		t.Fatal("link.txt.txt not read")
	}
	// Saving from the window writes link.txt and drops the other one.
	if err := a.saveLink(testLink("u.example.org")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, linkName+".txt")); !os.IsNotExist(err) {
		t.Fatal("link.txt.txt kept")
	}
	if a.linkCfg.Zone != "u.example.org" {
		t.Fatal(a.linkCfg.Zone)
	}
}

func TestInsideArchive(t *testing.T) {
	temp := filepath.Join("C:", "Users", "me", "AppData", "Local", "Temp")
	for dir, want := range map[string]bool{
		filepath.Join(temp, "Temp1_passage-0.6.0-windows-amd64.zip", "passage-0.6.0-windows-amd64"): true,
		filepath.Join(temp, "passage"):                                   false,
		filepath.Join("C:", "Users", "me", "Downloads", "passage.zip.d"): false,
		temp: false,
	} {
		if got := insideArchive(dir, temp); got != want {
			t.Errorf("%s: %v, want %v", dir, got, want)
		}
	}
}

func (*fakePlatform) updateTarget() string             { return "" }
func (*fakePlatform) applyUpdate(string, string) error { return nil }

type linkPlatform struct {
	fakePlatform
	opened string
	can    bool
}

func (p *linkPlatform) canOpen(string) bool { return p.can }
func (p *linkPlatform) open(s string) error { p.opened = s; return nil }

func TestOpenTgOnlyWithAHandler(t *testing.T) {
	p := &linkPlatform{}
	a := newApp(t.TempDir(), "1.0.0", "", "en", p)
	if opened, err := a.call("openTg", nil); err != nil || opened != false || p.opened != "" {
		t.Fatal(opened, err, p.opened)
	}
	p.can = true
	if opened, err := a.call("openTg", nil); err != nil || opened != true || !strings.HasPrefix(p.opened, "tg://socks?server=") {
		t.Fatal(opened, err, p.opened)
	}
}

func v2Link(t *testing.T, mode, id string, priority uint16) string {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(nil)
	r := links.Route{ID: id, Type: "wss", Wire: "wss-v1", Priority: priority, Params: map[string]any{
		"endpoint": "wss://example.org/passage", "key": base64.StdEncoding.EncodeToString(pub)}}
	raw, err := links.Encode(links.Bundle{V: 2, Kind: "routes", Mode: mode, Routes: []links.Route{r}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := links.URI(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	return uri
}

func TestSaveRouteEdits(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir, "test", "", "en", nil)
	for _, l := range []string{v2Link(t, "merge", "aa", 20), v2Link(t, "merge", "bb", 30)} {
		if err := a.saveLink(l); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.saveRouteEdits([]savedRoute{{ID: "aa", Priority: 40, On: false}, {ID: "bb", Priority: 30, On: true}}); err != nil {
		t.Fatal(err)
	}
	got := savedRoutes(a.linkCfg)
	if len(got) != 2 || got[0].ID != "bb" || got[1].ID != "aa" || got[1].On || got[1].Priority != 40 {
		t.Fatalf("saved %+v", got)
	}
	if r := a.routes(); len(r) != 1 || r[0].ID != "bb" {
		t.Fatalf("routes %+v", r)
	}
	if len(a.settings.Edits) != 1 {
		t.Fatalf("unchanged route kept as an edit: %+v", a.settings.Edits)
	}
	// The edit survives a restart; a replace link clears it.
	b := newApp(dir, "test", "", "en", nil)
	if got := savedRoutes(b.linkCfg); got[1].On || got[1].Priority != 40 {
		t.Fatalf("not kept: %+v", got)
	}
	if err := b.saveLink(v2Link(t, "replace", "aa", 20)); err != nil {
		t.Fatal(err)
	}
	if got := savedRoutes(b.linkCfg); len(got) != 1 || !got[0].On || got[0].Priority != 20 || len(b.settings.Edits) != 0 {
		t.Fatalf("replace kept edits: %+v %+v", got, b.settings.Edits)
	}
}

func TestSettingsEmptySections(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		t.Run(lang, func(t *testing.T) {
			a := newApp(t.TempDir(), "test", "", lang, &fakePlatform{})
			if got := a.linkView(false); len(got.Routes) != 0 || got.RoutesEmpty != a.t.f("saved_routes_empty") {
				t.Fatalf("empty routes: %+v", got)
			}
			if got := a.view(); len(got.Subscriptions) != 0 || got.SubscriptionsEmpty != a.t.f("subscriptions_empty") {
				t.Fatalf("empty subscriptions: %+v", got)
			}
			a.linkCfg.Routes = []links.Route{{ID: "example", Type: "wss", Priority: 10}}
			if got := a.linkView(false); len(got.Routes) != 1 || got.RoutesEmpty != "" {
				t.Fatalf("saved route: %+v", got)
			}
			for _, closed := range []bool{false, true} {
				a.linkCfg.Subscriptions = []tunnel.Subscription{{ID: "example", Name: "Example", Closed: closed}}
				if got := a.view(); len(got.Subscriptions) != 1 || got.SubscriptionsEmpty != "" || got.Subscriptions[0].Closed != closed {
					t.Fatalf("subscription closed=%v: %+v", closed, got)
				}
			}
		})
	}
}

func TestReportRedactsAddressesAndSecrets(t *testing.T) {
	got := redact("password: two words\nread udp 192.0.2.4:40112->192.0.2.53:53: i/o timeout\n{\"sources\":[\"https://example.org/a\", \"https://example.org/b\"]}")
	for _, secret := range []string{"two words", "192.0.2.4", "example.org"} {
		if strings.Contains(got, secret) {
			t.Errorf("leaked %s in %q", secret, got)
		}
	}
	if !strings.Contains(got, "[addr]->192.0.2.53:53") {
		t.Errorf("destination was lost: %q", got)
	}
}

func TestReportMasksOwnIPv6Prefix(t *testing.T) {
	report := map[string]any{
		"dns_egress":        "192.0.2.4",
		"network_resolvers": []any{"[2001:db8:10::53]:53", "[2001:db8:20::53]:53"},
		"device_network":    map[string]any{"dns": []any{"[2001:db8:10::53]:53"}},
		"events":            []any{map[string]any{"msg": "read udp [2001:db8:10::8]:1234->192.0.2.53:53: timeout; via 2001:db8:10::4: refused; to dns-192.0.2.4.example.org"}},
	}
	maskReportAddresses(report, []netip.Prefix{netip.MustParsePrefix("2001:db8:10::/64"), netip.MustParsePrefix("192.0.2.4/32")})
	got, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"device_network":{"dns":["[addr]"]},"dns_egress":"[addr]","events":[{"msg":"read udp [addr]-\u003e192.0.2.53:53: timeout; via [addr]: refused; to dns-192.0.2.4.example.org"}],"network_resolvers":["[addr]","[2001:db8:20::53]:53"]}`
	if string(got) != want {
		t.Fatalf("report: %s", got)
	}
}

func TestReportRedactsEveryString(t *testing.T) {
	report := map[string]any{
		"error":        "token=private-value",
		"network_test": []string{"password=private-value"},
		"nested":       []any{map[string]any{"detail": "key=private-value"}},
	}
	maskReportAddresses(report, nil)
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "private-value") {
		t.Fatalf("secret in report: %s", raw)
	}
}

func TestLanguageSnapshotsDuringChanges(t *testing.T) {
	a := newApp(t.TempDir(), "1.0", "", "en", &fakePlatform{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 30 {
			a.setLanguage("ru")
			a.setLanguage("en")
		}
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			payload, err := a.call("init", nil)
			if err != nil {
				t.Error(err)
				return
			}
			v := payload.(map[string]any)
			lang := v["lang"].(string)
			if v["strings"].(tr).f("action_start") != tr(table(lang)).f("action_start") {
				t.Error("mixed language snapshot")
			}
			st := a.trayState()
			if st.Text.f("action_start") != tr(table(st.Lang)).f("action_start") {
				t.Error("mixed tray language")
			}
		}
	}()
	wg.Wait()
}
