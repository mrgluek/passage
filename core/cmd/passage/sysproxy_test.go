package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	links "github.com/mrgluek/passage-link/link"
)

// fakeProxy is the system's proxy settings in memory.
type fakeProxy struct {
	st      proxyState
	writes  []string
	readErr error
	setErr  error
}

func (f *fakeProxy) Read() (proxyState, error) { return f.st, f.readErr }
func (f *fakeProxy) SetAutoConfig(url string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.st.AutoConfigURL = url
	f.writes = append(f.writes, url)
	return nil
}

const oursURL = "http://127.0.0.1:21000/proxy.pac"

func TestEnableSetsWhenEmptyAndRestoreClearsIt(t *testing.T) {
	f := &fakeProxy{st: proxyState{AutoDetect: true}}
	b, err := enableSystemPAC(f, oursURL)
	if err != nil || f.st.AutoConfigURL != oursURL || !b.Active || b.Ours != oursURL || b.PrevURL != "" || !b.AutoDetect {
		t.Fatalf("%+v %v %+v", b, err, f.st)
	}
	changed, err := restoreSystemPAC(f, b)
	if err != nil || !changed || f.st.AutoConfigURL != "" || !f.st.AutoDetect {
		t.Fatalf("restore: %v %v %+v", changed, err, f.st)
	}
}

func TestEnableRefusesForeignSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		st   proxyState
		want error
	}{
		"foreign PAC": {proxyState{AutoConfigURL: "http://example.org/other.pac"}, errSysForeignPAC},
		"manual":      {proxyState{ProxyEnable: true, ProxyServer: "example.org:3128"}, errSysManualProxy},
		"policy":      {proxyState{Policy: "ProxyServer"}, errSysPolicy},
	} {
		f := &fakeProxy{st: tc.st}
		if _, err := enableSystemPAC(f, oursURL); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
		if len(f.writes) != 0 || f.st != tc.st {
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
	f := &fakeProxy{}
	b, _ := enableSystemPAC(f, oursURL)
	f.st.AutoConfigURL = "http://example.org/user.pac" // the user changed it meanwhile
	changed, err := restoreSystemPAC(f, b)
	if err != nil || changed || f.st.AutoConfigURL != "http://example.org/user.pac" {
		t.Fatalf("restore touched a foreign address: %v %v %q", changed, err, f.st.AutoConfigURL)
	}
	f.st.AutoConfigURL = oursURL
	f.readErr = errors.New("registry unavailable")
	if _, err := restoreSystemPAC(f, b); err == nil {
		t.Fatal("read error hidden")
	}
}

func TestEnableAgainAfterACrashKeepsTheOriginalState(t *testing.T) {
	f := &fakeProxy{st: proxyState{AutoConfigURL: oursURL}}
	b, err := enableSystemPAC(f, oursURL)
	if err != nil || b.PrevURL != "" || len(f.writes) != 0 {
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
	f := &fakeProxy{}
	a, dir := browserApp(t, f, true)
	if err := a.setSystemPAC(true); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	url := a.pacURLLocked()
	a.mu.Unlock()
	if f.st.AutoConfigURL != url || !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/proxy.pac") {
		t.Fatalf("address %q", f.st.AutoConfigURL)
	}
	s := reload(t, dir)
	if !s.SystemPAC || !s.SysBackup.Active || s.SysBackup.Ours != url || s.BrowserPort == 0 || s.BrowserPort == s.Port {
		t.Fatalf("settings not saved: %+v", s)
	}
	if err := a.setSystemPAC(false); err != nil {
		t.Fatal(err)
	}
	if f.st.AutoConfigURL != "" {
		t.Fatal("not restored")
	}
	if s := reload(t, dir); s.SystemPAC || s.SysBackup.Active {
		t.Fatalf("settings after off: %+v", s)
	}
}

func TestSystemSwitchRefusalIsShownAndChangesNothing(t *testing.T) {
	f := &fakeProxy{st: proxyState{AutoConfigURL: "http://example.org/other.pac"}}
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
	f := &fakeProxy{}
	a, dir := browserApp(t, f, true)
	a.setSystemPAC(true)
	a.mu.Lock()
	a.restoreSystemPACLocked("quit")
	a.mu.Unlock()
	if f.st.AutoConfigURL != "" {
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
	if f.st.AutoConfigURL == "" || !reload(t, dir).SysBackup.Active {
		t.Fatal("the next start did not apply the switch again")
	}
}

// The app was killed with the address set: the backup is in passage.ini.
func TestCrashRecovery(t *testing.T) {
	f := &fakeProxy{}
	a, dir := browserApp(t, f, true)
	a.setSystemPAC(true)
	ours := f.st.AutoConfigURL

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
	if f.st.AutoConfigURL != "" || reload(t, dir).SysBackup.Active {
		t.Fatal("crash left the address behind")
	}

	// Backup present, switch still wanted, address still ours: it continues.
	f.st.AutoConfigURL = ours
	s.SystemPAC, s.SysBackup = true, sysBackup{Active: true, Ours: ours}
	s.save(filepath.Join(dir, iniName))
	c := newApp(dir, "test", "", "en", &fakePlatform{})
	c.sysProxy = f
	c.mu.Lock()
	c.recoverSystemPACLocked()
	c.mu.Unlock()
	if f.st.AutoConfigURL != ours || !reload(t, dir).SysBackup.Active {
		t.Fatal("a wanted switch was dropped")
	}

	// Someone replaced the address: the backup is dropped, their value stays.
	f.st.AutoConfigURL = "http://example.org/user.pac"
	d := newApp(dir, "test", "", "en", &fakePlatform{})
	d.sysProxy = f
	d.mu.Lock()
	d.recoverSystemPACLocked()
	d.mu.Unlock()
	if f.st.AutoConfigURL != "http://example.org/user.pac" || reload(t, dir).SysBackup.Active {
		t.Fatal("a foreign address was touched")
	}
}

func TestRestoreFailureKeepsTheBackup(t *testing.T) {
	f := &fakeProxy{}
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
	a, _ := browserApp(t, &fakeProxy{}, false)
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
	a, dir := browserApp(t, &fakeProxy{}, true)
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
		for _, k := range []string{"browser_title", "browser_text", "browser_pac", "browser_sites", "browser_manual", "browser_system", "browser_system_help", "browser_refuse_pac", "browser_refuse_manual", "browser_refuse_policy"} {
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
	f := &fakeProxy{st: proxyState{Policy: "ProxyServer"}}
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
