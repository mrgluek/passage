package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// macFake is a macOS system with several network services.
func macFake(services ...proxyService) *fakeProxy {
	return &fakeProxy{mac: true, st: proxyState{Services: services}}
}

const oldPAC = "http://example.org/old.pac"

func TestMacEnableSetsEveryServiceAndRestoreKeepsOursUnused(t *testing.T) {
	f := macFake(
		proxyService{Name: "Wi-Fi"},
		proxyService{Name: "Ethernet", AutoConfigURL: oldPAC},                 // stored, unused
		proxyService{Name: "Bridge", AutoConfigURL: oursURL},                  // left by an earlier run
		proxyService{Name: "USB", AutoConfigURL: oursURL, AutoConfigOn: true}, // a crashed run
	)
	b, err := enableSystemPAC(f, oursURL)
	if err != nil || len(b.Services) != 4 {
		t.Fatalf("%+v %v", b, err)
	}
	for _, s := range f.st.Services {
		if !s.ours(oursURL) {
			t.Fatalf("%s not set: %+v", s.Name, s)
		}
	}
	if !slices.Equal(f.writes, []string{"Wi-Fi=" + oursURL, "Ethernet=" + oursURL, "Bridge=" + oursURL}) {
		t.Fatalf("writes %v", f.writes)
	}
	prev := map[string]string{}
	for _, s := range b.Services {
		prev[s.Name] = s.PrevURL
	}
	if prev["Wi-Fi"] != "" || prev["Ethernet"] != oldPAC || prev["Bridge"] != "" || prev["USB"] != "" {
		t.Fatalf("backup %+v", b.Services)
	}
	changed, err := restoreSystemPAC(f, b)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	want := []proxyService{
		{Name: "Wi-Fi", AutoConfigURL: oursURL}, // networksetup cannot remove it: stays, off
		{Name: "Ethernet", AutoConfigURL: oldPAC},
		{Name: "Bridge", AutoConfigURL: oursURL},
		{Name: "USB", AutoConfigURL: oursURL},
	}
	if !slices.Equal(f.st.Services, want) {
		t.Fatalf("after restore %+v", f.st.Services)
	}
	// The address left unused is ours again next time, with nothing to restore.
	b, err = enableSystemPAC(f, oursURL)
	if err != nil || b.Services[0].PrevURL != "" {
		t.Fatalf("%+v %v", b, err)
	}
}

func TestMacRefusesWhenAnyServiceHasItsOwnSettings(t *testing.T) {
	for name, odd := range map[string]proxyService{
		"foreign PAC": {Name: "Ethernet", AutoConfigURL: oldPAC, AutoConfigOn: true},
		"web proxy":   {Name: "Ethernet", ProxyEnable: true, ProxyServer: "example.org:3128"},
	} {
		f := macFake(proxyService{Name: "Wi-Fi"}, odd)
		before := slices.Clone(f.st.Services)
		if _, err := enableSystemPAC(f, oursURL); err == nil || noticeKey(err) == "browser_refuse_error" {
			t.Errorf("%s: %v", name, err)
		}
		if len(f.writes) != 0 || !slices.Equal(f.st.Services, before) {
			t.Errorf("%s: changed %v", name, f.writes)
		}
	}
	f := macFake(proxyService{Name: "Wi-Fi"})
	f.st.Policy = "profile: ProxyType"
	_, err := enableSystemPAC(f, oursURL)
	if !errors.Is(err, errSysPolicy) || noticeArg(err) != "profile: ProxyType" || len(f.writes) != 0 {
		t.Fatal(err)
	}
}

func TestMacFailedWriteUndoesTheOthers(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"}, proxyService{Name: "Ethernet"})
	f.failOn = "Ethernet"
	if _, err := enableSystemPAC(f, oursURL); err == nil {
		t.Fatal("write error hidden")
	}
	if f.st.Services[0].AutoConfigOn {
		t.Fatal("first service left on after a failed enable")
	}
}

func TestMacRestoreOnlyServicesStillOurs(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"}, proxyService{Name: "Ethernet"})
	b, _ := enableSystemPAC(f, oursURL)
	f.st.Services[1] = proxyService{Name: "Ethernet", AutoConfigURL: oldPAC, AutoConfigOn: true} // the user's own
	f.st.Services = append(f.st.Services[:1], f.st.Services[1], proxyService{Name: "Bridge"})
	f.writes = nil
	if changed, err := restoreSystemPAC(f, b); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if !slices.Equal(f.writes, []string{"Wi-Fi="}) || !f.st.Services[1].AutoConfigOn {
		t.Fatalf("writes %v, %+v", f.writes, f.st.Services)
	}
}

func TestMacNewServiceIsAddedOnNetworkChange(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"})
	f.mac = true
	a, dir := browserApp(t, f, true)
	if err := a.setSystemPAC(true); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	ours := a.pacURLLocked()
	a.mu.Unlock()
	f.st.Services = append(f.st.Services,
		proxyService{Name: "USB Ethernet 2"},
		proxyService{Name: "Phone", ProxyEnable: true, ProxyServer: "example.org:8080"})
	a.extendSystemPAC("network change")
	if !f.st.Services[1].ours(ours) || f.st.Services[2].AutoConfigURL != "" {
		t.Fatalf("%+v", f.st.Services)
	}
	s := reload(t, dir)
	if !s.SysBackup.Active || len(s.SysBackup.Services) != 2 || s.SysBackup.Services[1].Name != "USB Ethernet 2" {
		t.Fatalf("backup not saved: %+v", s.SysBackup)
	}
	// Killed now: the next start without the wish restores both from passage.ini.
	s.SystemPAC = false
	s.save(dir + "/" + iniName)
	b := newApp(dir, "test", "", "en", &fakePlatform{})
	b.sysProxy = f
	b.recoverSystemPAC()
	b.applySystemPACAtStart()
	if f.st.anyOurs(ours) || reload(t, dir).SysBackup.Active {
		t.Fatalf("crash recovery left ours on: %+v", f.st.Services)
	}
}

func TestMacBackupRoundTripsThroughTheIni(t *testing.T) {
	a, dir := browserApp(t, macFake(), true)
	a.settings.SystemPAC = true
	want := []serviceBackup{
		{Name: "Wi-Fi"}, {Name: "USB 10/100/1000 LAN = 2", PrevURL: oldPAC}, {Name: "tab\there"},
		{Name: " Renamed  "}, {Name: `Quote "x" \ y`, PrevURL: "http://example.org/a b.pac"},
	}
	a.settings.SysBackup = sysBackup{Active: true, Ours: oursURL, Services: slices.Clone(want)}
	a.saveSettings()
	got := reload(t, dir).SysBackup
	if !got.Active || got.Ours != oursURL || !slices.Equal(got.Services, want) {
		t.Fatalf("%+v", got)
	}
}

func TestParseServiceOrder(t *testing.T) {
	out := `An asterisk (*) denotes that a network service is disabled.
(1) USB 10/100 LAN
(Hardware Port: USB 10/100 LAN, Device: en5)

(*) Old Ethernet
(Hardware Port: Ethernet, Device: en1)

(2) Wi-Fi
(Hardware Port: Wi-Fi, Device: en0)

(3) Some VPN (work)
(Hardware Port: com.example.vpn, Device: )
`
	list := parseServiceOrder(out)
	if len(list) != 4 || list[1].Name != "Old Ethernet" || !list[1].Disabled || list[3].Name != "Some VPN (work)" || list[3].Device != "" || list[0].Device != "en5" {
		t.Fatalf("%+v", list)
	}
	if got := proxyServices(list); !slices.Equal(got, []string{"USB 10/100 LAN", "Wi-Fi"}) {
		t.Fatalf("%v", got)
	}
}

func TestParseNetworksetupOutput(t *testing.T) {
	if u, on := parseAutoProxy("URL: (null)\nEnabled: No\n"); u != "" || on {
		t.Fatal(u, on)
	}
	if u, on := parseAutoProxy("URL: http://127.0.0.1:2/proxy.pac\nEnabled: Yes\n"); u != "http://127.0.0.1:2/proxy.pac" || !on {
		t.Fatal(u, on)
	}
	if on, server := parseManualProxy("Enabled: No\nServer: 127.0.0.1\nPort: 12334\nAuthenticated Proxy Enabled: 0\n"); on || server != "127.0.0.1:12334" {
		t.Fatal(on, server)
	}
	if on, _ := parseManualProxy("Enabled: Yes\nServer: example.org\nPort: 3128\n"); !on {
		t.Fatal("manual proxy missed")
	}
}

func TestManagedProxyKeys(t *testing.T) {
	xml := func(body string) []byte {
		return []byte(`<?xml version="1.0" encoding="UTF-8"?><plist><dict>` + body + `</dict></plist>`)
	}
	for _, tc := range []struct {
		data []byte
		want string
	}{
		{xml(`<key>PayloadType</key><string>com.apple.dnsSettings.managed</string><key>ServerURL</key><string>https://example.org/</string>`), ""},
		{xml(`<key>ProxyTypeName</key><string>x</string>`), ""},
		{xml(`<key>PayloadType</key><string>com.apple.proxy.http.global</string>`), "com.apple.proxy.http.global"},
		{xml(`<key>ProxyType</key><string>Auto</string>`), "ProxyType"},
		{xml(`<key>ProxyType</key><string>Manual</string><key>ProxyServer</key><string>example.org</string>`), "ProxyType"},
		// A network profile that leaves proxies alone says None.
		{xml(`<key>PayloadType</key><string>com.apple.wifi.managed</string><key>SSID_STR</key><string>x</string><key>ProxyType</key><string>None</string>`), ""},
		// Proxy keys without a type (as in a VPN payload) do not count alone.
		{xml(`<key>HTTPProxy</key><string>example.org</string><key>ProxyServer</key><string>example.org</string>`), ""},
		{[]byte("bplist00\xd1\x01\x02_DNSSettings"), ""}, // binary lists are converted to XML first
	} {
		if got := managedProxy(tc.data); got != tc.want {
			t.Errorf("%q: %q", strings.TrimSpace(string(tc.data)), got)
		}
	}
}

// An address left unused by another Passage (another port) or another
// program is not in use: it is replaced and put back exactly. Only an
// enabled one refuses.
func TestMacDisabledForeignAddressIsAllowedAndPutBack(t *testing.T) {
	const other = "http://127.0.0.1:29033/proxy.pac"
	f := macFake(proxyService{Name: "Wi-Fi", AutoConfigURL: other}, proxyService{Name: "Ethernet", AutoConfigURL: other})
	b, err := enableSystemPAC(f, oursURL)
	if err != nil || !f.st.anyOurs(oursURL) || b.Services[0].PrevURL != other {
		t.Fatalf("%+v %v %+v", b, err, f.st.Services)
	}
	if _, err := restoreSystemPAC(f, b); err != nil {
		t.Fatal(err)
	}
	want := []proxyService{{Name: "Wi-Fi", AutoConfigURL: other}, {Name: "Ethernet", AutoConfigURL: other}}
	if !slices.Equal(f.st.Services, want) {
		t.Fatalf("not put back as it was: %+v", f.st.Services)
	}

	f = macFake(proxyService{Name: "Wi-Fi", AutoConfigURL: other, AutoConfigOn: true}, proxyService{Name: "Ethernet"})
	if _, err := enableSystemPAC(f, oursURL); !errors.Is(err, errSysForeignPAC) || len(f.writes) != 0 {
		t.Fatalf("an address in use was not refused: %v %v", err, f.writes)
	}
}

// A service that is gone does not block restoring the others, and it is
// reported, not kept forever.
func TestMacRestoreSkipsAServiceThatIsGone(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"}, proxyService{Name: "USB LAN"}, proxyService{Name: "Ethernet"})
	b, _ := enableSystemPAC(f, oursURL)
	f.st.Services = slices.Delete(f.st.Services, 1, 2) // the adapter's service was deleted
	res, err := restoreServices(macReader{f}, b)
	if err != nil || !res.changed || res.missing != 1 || f.st.anyOurs(oursURL) {
		t.Fatalf("%+v %v %+v", res, err, f.st.Services)
	}
}

func TestRestoreFailureOnUncheckIsShownAndKeepsTheState(t *testing.T) {
	f := winFake(proxyService{})
	a, dir := browserApp(t, f, true)
	a.setSystemPAC(true)
	f.setErr = errors.New("access denied")
	if err := a.setSystemPAC(false); err != nil {
		t.Fatal(err)
	}
	v := a.view().Browser
	if !v.On || !strings.Contains(v.Notice, "access denied") {
		t.Fatalf("failure hidden: %+v", v)
	}
	if s := reload(t, dir); s.SystemPAC || !s.SysBackup.Active {
		t.Fatalf("settings: %+v", s)
	}
}

// blockingProxy never finishes a write, as a hung networksetup would.
type blockingProxy struct{ *fakeProxy }

func (blockingProxy) SetAutoConfig(string, string, bool) error { select {} }

func TestQuitRestoreIsBounded(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"})
	a, dir := browserApp(t, f, true)
	a.setSystemPAC(true)
	a.mu.Lock()
	a.sysProxy = blockingProxy{f}
	a.mu.Unlock()
	start := time.Now()
	a.restoreOnQuit(100 * time.Millisecond)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("quit waited %v", d)
	}
	if !reload(t, dir).SysBackup.Active {
		t.Fatal("backup lost after an unfinished restore")
	}
}

func TestOldTabSeparatedServiceBackupStillLoads(t *testing.T) {
	if sb, ok := parseServiceBackup("Wi-Fi\thttp://example.org/old.pac"); !ok || sb.Name != "Wi-Fi" || sb.PrevURL != oldPAC {
		t.Fatal(sb, ok)
	}
	if sb, ok := parseServiceBackup("Wi-Fi"); !ok || sb.Name != "Wi-Fi" || sb.PrevURL != "" {
		t.Fatal(sb, ok)
	}
}

// Killed after setting a service but before saving the backup: the next
// start turns that service's address off.
func TestStartTurnsOffLeftoversOutsideTheBackup(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"}, proxyService{Name: "Ethernet"})
	a, dir := browserApp(t, f, true)
	a.setSystemPAC(true)
	a.mu.Lock()
	ours := a.pacURLLocked()
	a.mu.Unlock()
	s := reload(t, dir)
	s.SystemPAC, s.SysBackup = true, sysBackup{Active: true, Ours: ours, Services: []serviceBackup{{Name: "Wi-Fi"}}}
	s.SysPending = true // the write was cut short
	s.save(dir + "/" + iniName)
	b := newApp(dir, "test", "", "en", &fakePlatform{})
	b.sysProxy = f
	b.sweepSystemPAC()
	if !f.st.Services[0].ours(ours) || f.st.Services[1].AutoConfigOn {
		t.Fatalf("%+v", f.st.Services)
	}
}

// The same address set by hand (no write of ours was cut short) stays.
func TestStartLeavesAnAddressSetByHand(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"})
	a, dir := browserApp(t, f, true)
	a.mu.Lock()
	a.ensureBrowserPortLocked()
	ours := a.pacURLLocked()
	a.mu.Unlock()
	f.st.Services[0] = proxyService{Name: "Wi-Fi", AutoConfigURL: ours, AutoConfigOn: true} // by hand
	b := newApp(dir, "test", "", "en", &fakePlatform{})
	b.sysProxy = f
	b.sweepSystemPAC()
	if !f.st.Services[0].ours(ours) {
		t.Fatal("an address set by hand was turned off")
	}
}

// unreadable is a macOS system where some services cannot be read now.
type unreadable struct {
	*fakeProxy
	unread []string
}

func (u unreadable) Read() (proxyState, error) {
	st, err := u.fakeProxy.Read()
	st.Services = slices.DeleteFunc(st.Services, func(s proxyService) bool { return slices.Contains(u.unread, s.Name) })
	st.Unread = len(u.unread)
	return st, err
}

func (u unreadable) ReadServices(names []string) ([]proxyService, []string, error) {
	found, missing, _ := macReader{u.fakeProxy}.ReadServices(names)
	found = slices.DeleteFunc(found, func(s proxyService) bool { return slices.Contains(u.unread, s.Name) })
	if len(u.unread) > 0 {
		return found, missing, errors.New("timed out")
	}
	return found, missing, nil
}

func TestUnreadableServicesAtStartKeepTheBackup(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"})
	a, dir := browserApp(t, f, true)
	a.setSystemPAC(true)
	b := newApp(dir, "test", "", "en", &fakePlatform{})
	b.sysProxy = unreadable{f, []string{"Wi-Fi"}}
	b.recoverSystemPAC()
	b.applySystemPACAtStart()
	if s := reload(t, dir); !s.SysBackup.Active || !s.SystemPAC {
		t.Fatalf("backup or wish dropped on a partial read: %+v", s.SysBackup)
	}
}

func TestEnableWithNothingReadableIsRefused(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"})
	if _, err := enableSystemPAC(unreadable{f, []string{"Wi-Fi"}}, oursURL); !errors.Is(err, errSysUnreadable) || len(f.writes) != 0 {
		t.Fatal(err)
	}
}

func TestOneUnreadableServiceDoesNotHoldUpTheOthers(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"}, proxyService{Name: "VPN-ish"})
	b, _ := enableSystemPAC(f, oursURL)
	res, err := restoreServices(unreadable{f, []string{"VPN-ish"}}, b)
	if err == nil || f.st.Services[0].AutoConfigOn || len(res.left) != 1 || res.left[0].Name != "VPN-ish" {
		t.Fatalf("%+v %v %+v", res, err, f.st.Services)
	}
}

// After a failed uncheck the panel stays, even with no sites listed.
func TestPanelStaysAfterAFailedUncheckWithoutSites(t *testing.T) {
	f := winFake(proxyService{})
	a, _ := browserApp(t, f, true)
	a.setSystemPAC(true)
	a.linkCfg.Sites = nil
	f.setErr = errors.New("access denied")
	a.setSystemPAC(false)
	if v := a.view().Browser; !v.Show || !v.On || v.Notice == "" {
		t.Fatalf("%+v", v)
	}
}

// Turning the switch on again while it is on keeps what the first time saved.
func TestSecondEnableKeepsTheBackup(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi", AutoConfigURL: oldPAC})
	a, _ := browserApp(t, f, true)
	a.setSystemPAC(true)
	a.setSystemPAC(true)
	a.setSystemPAC(false)
	if s := f.st.Services[0]; s.AutoConfigURL != oldPAC || s.AutoConfigOn {
		t.Fatalf("the address before was lost: %+v", s)
	}
}

func TestServiceListWithoutServicesHasNoEmptyName(t *testing.T) {
	known := parseServiceNames("An asterisk (*) denotes that a network service is disabled.\nWi-Fi\n*Bridge\r\n\n")
	if len(known) != 2 || !known["Wi-Fi"] || !known["Bridge"] || known[""] {
		t.Fatalf("%v", known)
	}
	if known := parseServiceNames("An asterisk (*) denotes that a network service is disabled.\n"); len(known) != 0 {
		t.Fatalf("%v", known)
	}
}

// A start that cannot read any service does not call the address already set.
func TestReapplyWithNothingReadableIsAnError(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"})
	b, _ := enableSystemPAC(f, oursURL)
	set, already, refusal, err := reapplySystemPAC(unreadable{f, []string{"Wi-Fi"}}, &b)
	if !errors.Is(err, errSysUnreadable) || set != 0 || already != 0 || refusal != nil {
		t.Fatalf("%d %d %v %v", set, already, refusal, err)
	}
}

// A write that failed may still have been applied: the pending mark stays
// for the next start to sweep.
func TestFailedExtendKeepsThePendingMark(t *testing.T) {
	f := macFake(proxyService{Name: "Wi-Fi"})
	a, _ := browserApp(t, f, true)
	if err := a.setSystemPAC(true); err != nil {
		t.Fatal(err)
	}
	f.st.Services = append(f.st.Services, proxyService{Name: "USB Ethernet 2"})
	f.failOn = "USB Ethernet 2"
	a.extendSystemPAC("network change")
	a.mu.Lock()
	pending := a.settings.SysPending
	a.mu.Unlock()
	if !pending {
		t.Fatal("pending mark cleared after a failed write")
	}
}
