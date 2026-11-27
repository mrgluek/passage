package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
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
	a.mu.Lock()
	a.extendSystemPACLocked("network change")
	a.mu.Unlock()
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
	b.mu.Lock()
	b.recoverSystemPACLocked()
	b.mu.Unlock()
	if f.st.anyOurs(ours) || reload(t, dir).SysBackup.Active {
		t.Fatalf("crash recovery left ours on: %+v", f.st.Services)
	}
}

func TestMacBackupRoundTripsThroughTheIni(t *testing.T) {
	a, dir := browserApp(t, macFake(), true)
	a.settings.SystemPAC = true
	a.settings.SysBackup = sysBackup{Active: true, Ours: oursURL, Services: []serviceBackup{
		{Name: "Wi-Fi"}, {Name: "USB 10/100/1000 LAN = 2", PrevURL: oldPAC}, {Name: "tab\there"},
	}}
	a.saveSettings()
	got := reload(t, dir).SysBackup
	want := []serviceBackup{{Name: "Wi-Fi"}, {Name: "USB 10/100/1000 LAN = 2", PrevURL: oldPAC}, {Name: "tab here"}}
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
		{[]byte("bplist00\xd1\x01\x02_\x10\x18ProxyAutoConfigURLString"), "ProxyAutoConfigURLString"},
		{[]byte("bplist00\xd1\x01\x02_DNSSettings"), ""},
	} {
		if got := managedProxy(tc.data); got != tc.want {
			t.Errorf("%q: %q", strings.TrimSpace(string(tc.data)), got)
		}
	}
}
