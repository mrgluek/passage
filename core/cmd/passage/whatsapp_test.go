package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

func TestWhatsAppPortChoice(t *testing.T) {
	for _, start := range []int{-1, 0, maxPort - minPort - 1, maxPort - minPort} {
		seen := map[int]bool{}
		port, err := chooseWhatsAppPort(start, []int{minPort, minPort + 1}, func(p int) bool {
			if p < minPort || p >= maxPort || p == minPort || p == minPort+1 {
				t.Fatalf("invalid candidate %d", p)
			}
			seen[p] = true
			return p == maxPort-1
		})
		if err != nil || port != maxPort-1 || len(seen) == 0 {
			t.Fatalf("choice = %d, %v", port, err)
		}
	}
	calls := 0
	if port, err := chooseWhatsAppPort(4, []int{minPort}, func(int) bool { calls++; return false }); port != 0 || err == nil || calls != maxPort-minPort-1 {
		t.Fatalf("exhaustion = %d, %v, %d calls", port, err, calls)
	}
}

func TestWhatsAppPortsPersistAndReuse(t *testing.T) {
	s := defaultSettings()
	s.Port = minPort
	if err := s.ensureWhatsAppPorts(func(p int) bool { return p == minPort+1 || p == maxPort-1 }); err != nil {
		t.Fatal(err)
	}
	if s.WhatsAppChatPort == s.WhatsAppMediaPort || s.WhatsAppChatPort == s.Port || s.WhatsAppMediaPort == s.Port {
		t.Fatalf("ports: %+v", s)
	}
	before := s
	if err := s.ensureWhatsAppPorts(func(int) bool { t.Fatal("saved ports were checked again"); return false }); err != nil {
		t.Fatal(err)
	}
	if s.WhatsAppChatPort != before.WhatsAppChatPort || s.WhatsAppMediaPort != before.WhatsAppMediaPort {
		t.Fatal("ports changed")
	}
	s.WhatsApp = true
	path := filepath.Join(t.TempDir(), iniName)
	if err := s.save(path); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := loadSettings(path)
	if err != nil || !exists || !loaded.WhatsApp || loaded.WhatsAppChatPort != s.WhatsAppChatPort || loaded.WhatsAppMediaPort != s.WhatsAppMediaPort {
		t.Fatalf("round trip: %+v, %v", loaded, err)
	}
	for i := 0; i < 100; i++ {
		s.newAccess()
		if s.Port == s.WhatsAppChatPort || s.Port == s.WhatsAppMediaPort {
			t.Fatal("new SOCKS port collides")
		}
	}
}

func TestWhatsAppDefaultsAndMissingKeys(t *testing.T) {
	if s := defaultSettings(); s.WhatsApp || s.WhatsAppChatPort != 0 || s.WhatsAppMediaPort != 0 {
		t.Fatal("WhatsApp must default off without allocating ports")
	}
	path := filepath.Join(t.TempDir(), iniName)
	for _, contents := range []string{"port=21000\n", "whatsapp=false\n", "whatsapp=invalid\n"} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		s, _, err := loadSettings(path)
		if err != nil || s.WhatsApp || s.WhatsAppChatPort != 0 || s.WhatsAppMediaPort != 0 {
			t.Fatalf("missing keys: %+v, %v", s, err)
		}
	}
}

func TestWhatsAppInvalidPortsAndExhaustion(t *testing.T) {
	for _, ports := range [][2]int{{0, 0}, {1, 65536}, {21000, 21000}, {22000, 22001}} {
		s := Settings{Port: 22000, WhatsAppChatPort: ports[0], WhatsAppMediaPort: ports[1]}
		if err := s.ensureWhatsAppPorts(func(int) bool { return true }); err != nil {
			t.Fatal(err)
		}
		if s.WhatsAppChatPort < minPort || s.WhatsAppChatPort >= maxPort || s.WhatsAppMediaPort < minPort || s.WhatsAppMediaPort >= maxPort || s.WhatsAppChatPort == s.WhatsAppMediaPort || s.WhatsAppChatPort == s.Port || s.WhatsAppMediaPort == s.Port {
			t.Fatalf("bad ports %+v", s)
		}
	}
	s := Settings{Port: 21000}
	if err := s.ensureWhatsAppPorts(func(int) bool { return false }); err == nil || s.WhatsAppChatPort != 0 || s.WhatsAppMediaPort != 0 {
		t.Fatalf("exhaustion changed settings: %+v, %v", s, err)
	}
	calls := 0
	if err := s.ensureWhatsAppPorts(func(int) bool { calls++; return calls == 1 }); err == nil || s.WhatsAppChatPort != 0 || s.WhatsAppMediaPort != 0 {
		t.Fatal("partial allocation persisted")
	}
}

func TestWhatsAppForwards(t *testing.T) {
	s := Settings{WhatsAppChatPort: 21001, WhatsAppMediaPort: 21002}
	if got := whatsappForwards(s); len(got) != 0 {
		t.Fatalf("disabled: %+v", got)
	}
	s.WhatsApp = true
	want := []tunnel.Forward{
		{Name: "whatsapp-chat", Listen: host + ":21001", Target: "g.whatsapp.net:5222"},
		{Name: "whatsapp-media", Listen: host + ":21002", Target: "whatsapp.net:443"},
	}
	if got := whatsappForwards(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("forwards: %+v", got)
	}
}

func TestWhatsAppStatus(t *testing.T) {
	stamp := time.Date(2026, 1, 2, 13, 4, 0, 0, time.Local)
	for _, tc := range []struct {
		name            string
		rows            []tunnel.ForwardStatus
		lines, warnings []string
		connected       bool
	}{
		{name: "missing", lines: []string{"Chat: waiting", "Media: waiting"}},
		{name: "up waiting", rows: []tunnel.ForwardStatus{{Name: "whatsapp-chat", Up: true}, {Name: "whatsapp-media", Up: true}}, lines: []string{"Chat: waiting", "Media: waiting"}},
		{name: "last connection", rows: []tunnel.ForwardStatus{{Name: "whatsapp-chat", Up: true, LastConn: &stamp}}, lines: []string{"Chat: last connection 13:04", "Media: waiting"}, connected: true},
		{name: "down", rows: []tunnel.ForwardStatus{{Name: "whatsapp-chat", Error: "busy\nextra"}, {Name: "whatsapp-media"}}, lines: []string{"Chat: waiting", "Media: waiting"}, warnings: []string{"Chat: listener unavailable (busy)", "Media: listener unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := formatWhatsAppStatus(table("en"), tc.rows)
			if !reflect.DeepEqual(got.Lines, tc.lines) || !reflect.DeepEqual(got.Warnings, tc.warnings) || got.Connected != tc.connected {
				t.Fatalf("status: %+v", got)
			}
		})
	}
}

func TestWhatsAppHomeAndSettings(t *testing.T) {
	for _, want := range []string{
		`const names = ["tab_delta", "tab_tg", "whatsapp", "tab_other"]`,
		`else if (tab === 2) renderWhatsApp(pane, v, true)`,
		`call("setWhatsApp", toggle.checked)`,
		`home && !v.whatsapp`, `para(t("whatsapp_turn_on"))`,
		`[[t("proxy_host"), "localhost"]]`, `t("whatsapp_chat_port"), v.whatsapp_chat_port`, `t("whatsapp_media_port"), v.whatsapp_media_port`,
		`if (value) row.onclick = () => copy(String(value)`,
		`home && v.running && v.whatsapp`, `status.warnings`, `status.hint`,
		`renderWhatsApp($("whatsappSettings"), v, false)`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("UI missing %q", want)
		}
	}
	access := strings.Index(indexHTML, `data-t="access_label"`)
	whatsapp := strings.Index(indexHTML, `id="whatsappSettings"`)
	updates := strings.Index(indexHTML, `id="updateSettings"`)
	if access < 0 || whatsapp < access || updates < whatsapp {
		t.Fatal("WhatsApp settings card must follow proxy access")
	}
}

func TestWhatsAppAppSwitchAndView(t *testing.T) {
	a := newApp(t.TempDir(), "test", "", "en", &fakePlatform{})
	a.settings.Running = false
	a.settings.Mode = tunnel.ModeOff
	port, err := chooseWhatsAppPort(0, nil, localPortBindable)
	if err != nil {
		t.Fatal(err)
	}
	a.settings.Port = port
	if _, err := a.call("whatsappPorts", nil); err != nil {
		t.Fatal(err)
	}
	if a.tun != nil || a.settings.WhatsApp {
		t.Fatal("opening controls started the feature")
	}
	ports := [2]int{a.settings.WhatsAppChatPort, a.settings.WhatsAppMediaPort}
	if err := a.setWhatsApp(true); err != nil {
		t.Fatal(err)
	}
	if a.tun != nil {
		t.Fatal("switch started a stopped router")
	}
	a.setRunning(true)
	defer a.setRunning(false)
	if a.lastErr != "" || a.tun == nil {
		t.Fatalf("start: %s", a.lastErr)
	}
	first := a.tun
	v := a.view()
	if !v.WhatsApp || v.WhatsAppChatPort != ports[0] || v.WhatsAppMediaPort != ports[1] || len(v.Forwards) != 2 || !v.Forwards[0].Up || !v.Forwards[1].Up || v.WhatsAppStatus.Hint {
		t.Fatalf("view: %+v", v)
	}
	a.whatsappStarted = time.Now().Add(-31 * time.Second)
	if !a.view().WhatsAppStatus.Hint {
		t.Fatal("missing delayed setup hint")
	}
	if err := a.setWhatsApp(true); err != nil {
		t.Fatal(err)
	}
	if a.tun != first {
		t.Fatal("unchanged switch restarted router")
	}
	if err := a.setWhatsApp(false); err != nil {
		t.Fatal(err)
	}
	if a.tun == first || a.tun == nil || len(a.view().Forwards) != 0 || a.view().WhatsAppStatus.Hint {
		t.Fatal("turning off did not restart without forwards")
	}
	loaded, _, err := loadSettings(a.path(iniName))
	if err != nil || loaded.WhatsApp || loaded.WhatsAppChatPort != ports[0] || loaded.WhatsAppMediaPort != ports[1] {
		t.Fatalf("saved switch: %+v, %v", loaded, err)
	}
}
