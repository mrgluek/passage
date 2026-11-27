package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	links "github.com/mrgluek/passage-link/link"
)

// fakeProxy is the system's proxy settings in memory. With mac set, turning
// auto-config off keeps the address, as networksetup does.
type fakeProxy struct {
	st      proxyState
	mac     bool
	writes  []string
	readErr error
	setErr  error
	failOn  string // a service whose writes fail
}

func (f *fakeProxy) Read() (proxyState, error) {
	st := f.st
	st.Services = slices.Clone(f.st.Services)
	return st, f.readErr
}

func (f *fakeProxy) SetAutoConfig(name, url string, on bool) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.failOn != "" && name == f.failOn {
		return errors.New("write failed")
	}
	for i := range f.st.Services {
		s := &f.st.Services[i]
		if s.Name != name {
			continue
		}
		if f.mac {
			if url != "" {
				s.AutoConfigURL = url
			}
			s.AutoConfigOn = on
		} else {
			s.AutoConfigURL, s.AutoConfigOn = url, url != ""
		}
	}
	f.writes = append(f.writes, name+"="+url)
	return nil
}

// winFake is a Windows system: one unnamed service.
func winFake(s proxyService) *fakeProxy {
	s.AutoConfigOn = s.AutoConfigURL != ""
	return &fakeProxy{st: proxyState{Services: []proxyService{s}}}
}

func (f *fakeProxy) url() string { return f.st.Services[0].AutoConfigURL }

func (f *fakeProxy) setURL(u string) {
	f.st.Services[0].AutoConfigURL, f.st.Services[0].AutoConfigOn = u, u != ""
}

const oursURL = "http://127.0.0.1:21000/proxy.pac"

func TestEnableSetsWhenEmptyAndRestoreClearsIt(t *testing.T) {
	f := winFake(proxyService{AutoDetect: true})
	b, err := enableSystemPAC(f, oursURL)
	if err != nil || f.url() != oursURL || !b.Active || b.Ours != oursURL || len(b.Services) != 1 || b.Services[0].PrevURL != "" || !b.Services[0].AutoDetect {
		t.Fatalf("%+v %v %+v", b, err, f.st)
	}
	changed, err := restoreSystemPAC(f, b)
	if err != nil || !changed || f.url() != "" || !f.st.Services[0].AutoDetect {
		t.Fatalf("restore: %v %v %+v", changed, err, f.st)
	}
}

func TestEnableRefusesForeignSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		st   proxyState
		want error
	}{
		"foreign PAC": {winFake(proxyService{AutoConfigURL: "http://example.org/other.pac"}).st, errSysForeignPAC},
		"manual":      {winFake(proxyService{ProxyEnable: true, ProxyServer: "example.org:3128"}).st, errSysManualProxy},
		"policy":      {proxyState{Services: []proxyService{{}}, Policy: "ProxyServer"}, errSysPolicy},
	} {
		f := &fakeProxy{st: tc.st}
		before := slices.Clone(tc.st.Services)
		if _, err := enableSystemPAC(f, oursURL); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
		if len(f.writes) != 0 || !slices.Equal(f.st.Services, before) {
			t.Errorf("%s: the system was changed", name)
		}
		if noticeKey(tc.want) == "browser_refuse_error" {
			t.Errorf("%s: no window text", name)
		}
	}
	if _, err := enableSystemPAC(nil, oursURL); !errors.Is(err, errSysUnsupported) {
		t.Fatal(err)
	}
}

func TestRestoreOnlyIfStillOurs(t *testing.T) {
	f := winFake(proxyService{})
	b, _ := enableSystemPAC(f, oursURL)
	f.setURL("http://example.org/user.pac") // the user changed it meanwhile
	changed, err := restoreSystemPAC(f, b)
	if err != nil || changed || f.url() != "http://example.org/user.pac" {
		t.Fatalf("restore touched a foreign address: %v %v %q", changed, err, f.url())
	}
	f.setURL(oursURL)
	f.readErr = errors.New("registry unavailable")
	if _, err := restoreSystemPAC(f, b); err == nil {
		t.Fatal("read error hidden")
	}
}

func TestEnableAgainAfterACrashKeepsTheOriginalState(t *testing.T) {
	f := winFake(proxyService{AutoConfigURL: oursURL})
	b, err := enableSystemPAC(f, oursURL)
	if err != nil || b.Services[0].PrevURL != "" || len(f.writes) != 0 {
		t.Fatalf("%+v %v %v", b, err, f.writes)
	}
}

func browserApp(t *testing.T, f *fakeProxy, withSites bool) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	a := newApp(dir, "test", "", "en", &fakePlatform{})
	a.sysProxy = f
	a.portFree = func(int) bool { return true }
	if withSites {
		a.linkCfg.Sites = []links.SiteGroup{{Via: "smart", Hosts: []string{"example.org", "example.net"}}}
	}
	return a, dir
}

func reload(t *testing.T, dir string) Settings {
	t.Helper()
	s, _, err := loadSettings(filepath.Join(dir, iniName))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSystemSwitchOnOffThroughTheApp(t *testing.T) {
	f := winFake(proxyService{})
	a, dir := browserApp(t, f, true)
	if err := a.setSystemPAC(true); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	url := a.pacURLLocked()
	a.mu.Unlock()
	if f.url() != url || !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/proxy.pac") {
		t.Fatalf("address %q", f.url())
	}
	s := reload(t, dir)
	if !s.SystemPAC || !s.SysBackup.Active || s.SysBackup.Ours != url || s.BrowserPort == 0 || s.BrowserPort == s.Port {
		t.Fatalf("settings not saved: %+v", s)
	}
	if err := a.setSystemPAC(false); err != nil {
		t.Fatal(err)
	}
	if f.url() != "" {
		t.Fatal("not restored")
	}
	if s := reload(t, dir); s.SystemPAC || s.SysBackup.Active {
		t.Fatalf("settings after off: %+v", s)
	}
}

func TestSystemSwitchRefusalIsShownAndChangesNothing(t *testing.T) {
	f := winFake(proxyService{AutoConfigURL: "http://example.org/other.pac"})
	a, dir := browserApp(t, f, true)
	if err := a.setSystemPAC(true); err != nil {
		t.Fatal(err)
	}
	v := a.view().Browser
	if v.Notice == "" || v.On || len(f.writes) != 0 {
		t.Fatalf("%+v", v)
	}
	if s := reload(t, dir); s.SystemPAC || s.SysBackup.Active {
		t.Fatal("a refused switch was saved")
	}
}

func TestQuitRestoresAndNextStartAppliesAgain(t *testing.T) {
	f := winFake(proxyService{})
	a, dir := browserApp(t, f, true)
	a.setSystemPAC(true)
	a.mu.Lock()
	a.restoreSystemPACLocked("quit")
	a.mu.Unlock()
	if f.url() != "" {
		t.Fatal("quit did not restore")
	}
	s := reload(t, dir)
	if !s.SystemPAC || s.SysBackup.Active {
		t.Fatalf("after quit: %+v", s)
	}
	b := newApp(dir, "test", "", "en", &fakePlatform{})
	b.sysProxy, b.portFree = f, func(int) bool { return true }
	b.mu.Lock()
	b.recoverSystemPACLocked()
	b.mu.Unlock()
	if f.url() == "" || !reload(t, dir).SysBackup.Active {
		t.Fatal("the next start did not apply the switch again")
	}
}

// The app was killed with the address set: the backup is in passage.ini.
func TestCrashRecovery(t *testing.T) {
	f := winFake(proxyService{})
	a, dir := browserApp(t, f, true)
	a.setSystemPAC(true)
	ours := f.url()

	// The user turned the switch off in passage.ini by hand: undo at start.
	s := reload(t, dir)
	s.SystemPAC = false
	if err := s.save(filepath.Join(dir, iniName)); err != nil {
		t.Fatal(err)
	}
	b := newApp(dir, "test", "", "en", &fakePlatform{})
	b.sysProxy = f
	b.mu.Lock()
	b.recoverSystemPACLocked()
	b.mu.Unlock()
	if f.url() != "" || reload(t, dir).SysBackup.Active {
		t.Fatal("crash left the address behind")
	}

	// Backup present, switch still wanted, address still ours: it continues.
	f.setURL(ours)
	s.SystemPAC, s.SysBackup = true, sysBackup{Active: true, Ours: ours, Services: []serviceBackup{{}}}
	s.save(filepath.Join(dir, iniName))
	c := newApp(dir, "test", "", "en", &fakePlatform{})
	c.sysProxy = f
	c.mu.Lock()
	c.recoverSystemPACLocked()
	c.mu.Unlock()
	if f.url() != ours || !reload(t, dir).SysBackup.Active {
		t.Fatal("a wanted switch was dropped")
	}

	// Someone replaced the address: the backup is dropped, their value stays.
	f.setURL("http://example.org/user.pac")
	d := newApp(dir, "test", "", "en", &fakePlatform{})
	d.sysProxy = f
	d.mu.Lock()
	d.recoverSystemPACLocked()
	d.mu.Unlock()
	if f.url() != "http://example.org/user.pac" || reload(t, dir).SysBackup.Active {
		t.Fatal("a foreign address was touched")
	}
}

func TestRestoreFailureKeepsTheBackup(t *testing.T) {
	f := winFake(proxyService{})
	a, dir := browserApp(t, f, true)
	a.setSystemPAC(true)
	f.setErr = errors.New("access denied")
	a.mu.Lock()
	a.restoreSystemPACLocked("quit")
	a.mu.Unlock()
	if !reload(t, dir).SysBackup.Active {
		t.Fatal("backup lost after a failed restore")
	}
}

func TestBrowserSectionOnlyWithSitesAndNoListenerWithout(t *testing.T) {
	a, _ := browserApp(t, winFake(proxyService{}), false)
	if v := a.view().Browser; v.Show || v.PAC != "" {
		t.Fatalf("%+v", v)
	}
	a.mu.Lock()
	cfg, _ := a.routerConfigLocked()
	port := a.settings.BrowserPort
	a.mu.Unlock()
	if cfg.BrowserListen != "" || port != 0 {
		t.Fatal("a listener without sites")
	}
	a.linkCfg.Sites = []links.SiteGroup{{Via: "tunnel", Hosts: []string{"example.org"}}}
	v := a.view().Browser
	if !v.Show || v.Sites != 1 || !v.System || !strings.Contains(v.PAC, "/proxy.pac") {
		t.Fatalf("%+v", v)
	}
	a.mu.Lock()
	cfg, _ = a.routerConfigLocked()
	a.mu.Unlock()
	if !strings.HasPrefix(cfg.BrowserListen, "127.0.0.1:") {
		t.Fatalf("%q", cfg.BrowserListen)
	}
	if strings.Contains(a.t.f("browser_text")+a.t.f("browser_system_help"), "example.org") {
		t.Fatal("site names in strings")
	}
}

func TestBrowserPortIsStableAndDistinct(t *testing.T) {
	a, dir := browserApp(t, winFake(proxyService{}), true)
	a.view()
	first := reload(t, dir).BrowserPort
	if first < minPort || first >= maxPort || first == a.settings.Port {
		t.Fatalf("port %d", first)
	}
	b := newApp(dir, "test", "", "en", &fakePlatform{})
	b.linkCfg.Sites = a.linkCfg.Sites
	b.view()
	if reload(t, dir).BrowserPort != first {
		t.Fatal("port changed between runs")
	}
	// A WhatsApp port never takes it.
	s := reload(t, dir)
	s.WhatsAppChatPort = first
	if err := s.ensureWhatsAppPorts(func(int) bool { return true }); err != nil || s.WhatsAppChatPort == first || s.WhatsAppMediaPort == first {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestBrowserSettingsKeysOldFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), iniName)
	os.WriteFile(path, []byte("[proxy]\nport=21000\nuser=u\npass=p\n"), 0600)
	s, _, err := loadSettings(path)
	if err != nil || s.BrowserPort != 0 || s.SystemPAC || s.SysBackup.Active {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestBrowserStringsInBothLanguages(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		tr := tr(table(lang))
		for _, k := range []string{"browser_title", "browser_text_system", "browser_text_manual", "browser_manual_system", "browser_manual_only", "tab_browser", "h_browser_done", "h_step_connected", "browser_pac", "browser_sites", "browser_system", "browser_system_help", "browser_refuse_pac", "browser_refuse_manual", "browser_refuse_policy"} {
			if tr.f(k) == "" || tr.f(k) == k {
				t.Errorf("%s %s missing", lang, k)
			}
		}
	}
}

func TestPolicyBlocksOnlyProxyPolicies(t *testing.T) {
	const is, cp = policyInternetSettings, policyControlPanel
	for _, tc := range []struct {
		root, path, name string
		dword            uint32
		isDWORD, want    bool
	}{
		// The stock value found on a Windows Server: nothing to do with proxies.
		{"HKLM", is, "CallLegacyWCMPolicies", 0, true, false},
		{"HKCU", is, "SecureProtocols", 2048, true, false},
		{"HKLM", is, "CertificateRevocation", 1, true, false},
		{"HKLM", is, "EnableAutoProxyResultCache", 0, true, false},
		{"HKCU", is, "AutoConfigURL", 0, false, true},
		{"HKLM", is, "autoconfigurl", 0, false, true},
		{"HKCU", is, "ProxyEnable", 1, true, true},
		{"HKLM", is, "ProxyServer", 0, false, true},
		{"HKLM", is, "ProxyOverride", 0, false, true},
		{"HKCU", is, "AutoDetect", 0, true, true},
		{"HKLM", is, "ProxySettingsPerUser", 1, true, false},
		{"HKLM", is, "ProxySettingsPerUser", 0, true, true},
		{"HKCU", is, "ProxySettingsPerUser", 0, true, false},
		{"HKCU", cp, "Proxy", 1, true, true},
		{"HKLM", cp, "Autoconfig", 1, true, true},
		{"HKLM", cp, "Proxy", 0, true, false},
		{"HKCU", cp, "Autoconfig", 0, true, false},
		{"HKCU", cp, "HomePage", 1, true, false},
		{"HKCU", `Software\Policies\Other`, "ProxyServer", 0, false, false},
	} {
		if got := policyBlocks(tc.root, tc.path, tc.name, tc.dword, tc.isDWORD); got != tc.want {
			t.Errorf("%s %s %s=%d: %v, want %v", tc.root, tc.path, tc.name, tc.dword, got, tc.want)
		}
	}
}

func TestPolicyRefusalNamesTheValue(t *testing.T) {
	f := &fakeProxy{st: proxyState{Services: []proxyService{{}}, Policy: "ProxyServer"}}
	_, err := enableSystemPAC(f, oursURL)
	if !errors.Is(err, errSysPolicy) || noticeArg(err) != "ProxyServer" {
		t.Fatal(err)
	}
	a, _ := browserApp(t, f, true)
	a.setSystemPAC(true)
	if n := a.view().Browser.Notice; !strings.Contains(n, "ProxyServer") {
		t.Fatalf("notice %q", n)
	}
}

// The home screen's connect step: a chat app, or the browser when the
// subscription lists sites. Done means the screen stops asking.
func TestConnectDoneRules(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		appSeen, hasSites, systemOn, confirmed, want bool
	}{
		{"nothing yet", false, false, false, false, false},
		{"chat app seen", true, false, false, false, true},
		{"sites, nothing set up", false, true, false, false, false},
		{"sites, system switch on", false, true, true, false, true},
		{"sites, address confirmed by hand", false, true, false, true, true},
		{"no sites: switch and confirmation do not count", false, false, true, true, false},
		{"chat app and browser", true, true, true, true, true},
	} {
		if got := connectDone(tc.appSeen, tc.hasSites, tc.systemOn, tc.confirmed); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHomeViewStopsOfferingAfterTheBrowserIsSetUp(t *testing.T) {
	f := winFake(proxyService{})
	a, dir := browserApp(t, f, true)
	if a.view().ConnectDone {
		t.Fatal("done before anything was set up")
	}
	a.confirmBrowser(true)
	v := a.view()
	if !v.ConnectDone || !v.Browser.Confirmed || v.AppSeen {
		t.Fatalf("%+v", v)
	}
	if !reload(t, dir).BrowserConfirmed {
		t.Fatal("confirmation not saved")
	}
	a.confirmBrowser(false)
	if a.view().ConnectDone {
		t.Fatal("still done after the confirmation was withdrawn")
	}
	if err := a.setSystemPAC(true); err != nil {
		t.Fatal(err)
	}
	if !a.view().ConnectDone {
		t.Fatal("the system switch does not finish the step")
	}
	// The sites go away from the subscription: the step is open again.
	a.linkCfg.Sites = nil
	if a.view().ConnectDone {
		t.Fatal("browser counts without sites")
	}
}

func TestHomeTabsRule(t *testing.T) {
	with, without := homeTabs(true), homeTabs(false)
	if len(with) != 4 || len(without) != 4 {
		t.Fatalf("more than four tabs: %v %v", with, without)
	}
	if with[3] != "tab_browser" || slices.Contains(with, "tab_other") {
		t.Fatalf("with sites: %v", with)
	}
	if without[3] != "tab_other" || slices.Contains(without, "tab_browser") {
		t.Fatalf("without sites: %v", without)
	}
	a, _ := browserApp(t, winFake(proxyService{}), true)
	if v := a.view(); !slices.Equal(v.Tabs, with) {
		t.Fatalf("view tabs %v", v.Tabs)
	}
	a.linkCfg.Sites = nil
	if v := a.view(); !slices.Equal(v.Tabs, without) {
		t.Fatalf("view tabs %v", v.Tabs)
	}
	for _, lang := range []string{"en", "ru"} {
		tr := tr(table(lang))
		for _, k := range append(slices.Clone(with), "tab_other", "other_app_toggle") {
			if tr.f(k) == "" || tr.f(k) == k {
				t.Errorf("%s: no text for %s", lang, k)
			}
		}
		for _, k := range []string{"h_step_app", "h_waiting", "h_reason_wait", "h_reason_ok", "h_reason_off", "h_step_app_done", "autostart_offer_text"} {
			if strings.Contains(strings.ToLower(tr.f(k)), "мессенджер") || strings.Contains(strings.ToLower(tr.f(k)), "chat app") {
				t.Errorf("%s %s still names messengers: %s", lang, k, tr.f(k))
			}
		}
	}
}

// Settings and the home tab draw the Browsers content with one function, so
// the checkbox, its text and refusals cannot drift apart.
func TestBrowserContentIsShared(t *testing.T) {
	if strings.Count(indexHTML, "browserSetup(") != 3 { // definition, settings, tab
		t.Fatalf("browserSetup used %d times", strings.Count(indexHTML, "browserSetup(")-1)
	}
	for _, gone := range []string{`call("setSystemPAC", true)`, "h_browser_turn_on"} {
		if strings.Contains(indexHTML, gone) {
			t.Errorf("a second system switch control is back: %s", gone)
		}
	}
	// The checkbox in the tab follows the same view field after a switch.
	a, _ := browserApp(t, winFake(proxyService{}), true)
	if a.view().Browser.On {
		t.Fatal("on before the switch")
	}
	a.setSystemPAC(true)
	if !a.view().Browser.On {
		t.Fatal("not checked after turning on")
	}
	a.setSystemPAC(false)
	if a.view().Browser.On {
		t.Fatal("still checked after turning off")
	}
}
