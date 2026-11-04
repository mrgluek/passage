package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	links "github.com/mrgluek/passage-link/link"
	"github.com/mrgluek/passage/core/tunnel"
)

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
	if err := s.save(path); err != nil {
		t.Fatal(err)
	}
	got, existed, err := loadSettings(path)
	if err != nil || !existed || got != s {
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

func TestLinkKinds(t *testing.T) {
	cfg := tunnel.Config{Routes: []links.Route{
		{ID: "b", Type: "iroh", Priority: 20},
		{ID: "a", Type: "wss", Priority: 10},
		{ID: "c", Type: "wss", Priority: 30},
	}}
	if got := linkKinds(cfg, "127.0.0.1:9050"); !slices.Equal(got, []string{"my", "wss", "iroh"}) {
		t.Fatal(got)
	}
	if got := linkKinds(tunnel.Config{Zone: "t.example.org"}, ""); !slices.Equal(got, []string{"soar"}) {
		t.Fatal(got)
	}
}

type fakePlatform struct{ copied string }

func (f *fakePlatform) copyText(s string) error { f.copied = s; return nil }
func (f *fakePlatform) open(string) error       { return nil }
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
	if !a.hasLink() || !slices.Equal(a.kinds(), []string{"soar"}) {
		t.Fatalf("kinds %v", a.kinds())
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
		if _, ok := texts["en"][key]; !ok {
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
