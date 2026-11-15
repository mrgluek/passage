package tunnel

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGroupApp(t *testing.T) {
	for _, tc := range []struct{ host, app string }{
		{"whatsapp.net", "whatsapp"}, {"g.whatsapp.net", "whatsapp"}, {"a.b.whatsapp.net", "whatsapp"},
		{"signal.org", "signal"}, {"chat.signal.org", "signal"}, {"SIGNAL.ORG.", "signal"}, {"G.WhatsApp.Net.", "whatsapp"},
		{"notwhatsapp.net", ""}, {"signal.org.example.net", ""}, {"notsignal.org", ""}, {"whatsapp.net.example.org", ""}, {"example.org", ""}, {"", ""},
	} {
		if got := groupApp("relay", tc.host); got != tc.app {
			t.Errorf("%q: got %q, want %q", tc.host, got, tc.app)
		}
		if got := groupApp("other", tc.host); got != "" {
			t.Errorf("non-relay %q: %q", tc.host, got)
		}
	}
}

func TestStatusGroupApp(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	tt, _ := evidenceTunnel(&now)
	tt.relays = map[string]*group{}
	for _, host := range []string{"g.whatsapp.net", "chat.signal.org", "example.org"} {
		tt.relays[host] = newGroup(host, kindRelay, RouteDirect)
	}
	status := tt.Status(0)
	for host, app := range map[string]string{"g.whatsapp.net": "whatsapp", "chat.signal.org": "signal", "example.org": ""} {
		g := status.Groups[host]
		if g.App != app {
			t.Errorf("%s: got %q, want %q", host, g.App, app)
		}
		b, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(b, &fields); err != nil {
			t.Fatal(err)
		}
		if value, present := fields["app"]; present != (app != "") || (present && value != app) {
			t.Errorf("app JSON for %s: %s", host, b)
		}
	}
}
