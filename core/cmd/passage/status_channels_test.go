package main

import (
	"github.com/mrgluek/passage/core/tunnel"
	"testing"
	"time"
)

func TestChannelLines(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	recent, old := now.Add(-time.Minute), now.Add(-16*time.Minute)
	for _, lang := range []string{"en", "ru"} {
		tr := tr(table(lang))
		direct, smart, mail, shared, tg := "direct", "smart", "Mail: ", "Route: ", "Tg: "
		if lang == "ru" {
			direct, smart, mail, shared = "напрямую", "smart (с подстройкой)", "Почта: ", "Маршрут: "
		}
		cases := []struct {
			name, mode string
			groups     map[string]tunnel.GroupStatus
			shown      map[string]time.Time
			want       string
		}{
			{name: "shared", want: shared + direct},
			{name: "direct", groups: map[string]tunnel.GroupStatus{"mail": {Kind: "relay", Route: "direct", LastConn: &recent}}, want: mail + direct},
			{name: "id", groups: map[string]tunnel.GroupStatus{"mail": {Kind: "relay", Route: "tunnel", RouteID: "berry", Via: "iroh", LastConn: &recent}}, want: mail + "berry"},
			{name: "smart", groups: map[string]tunnel.GroupStatus{"mail": {Kind: "relay", Route: "smart", LastConn: &recent}}, want: mail + smart},
			{name: "expired", groups: map[string]tunnel.GroupStatus{"mail": {Kind: "relay", Route: "direct", LastConn: &old}}, want: shared + direct},
			{name: "minimum", groups: map[string]tunnel.GroupStatus{"mail": {Kind: "relay", Route: "direct", LastConn: &old}}, shown: map[string]time.Time{"mail": now.Add(-4 * time.Minute)}, want: mail + direct},
			{name: "minimum expired", groups: map[string]tunnel.GroupStatus{"mail": {Kind: "relay", Route: "direct", LastConn: &old}}, shown: map[string]time.Time{"mail": now.Add(-5 * time.Minute)}, want: shared + direct},
			{name: "telegram only", groups: map[string]tunnel.GroupStatus{"not-a-kind": {Kind: "telegram", Route: "tunnel", RouteID: "berry", LastConn: &old}}, want: tg + "berry"},
			{name: "unused telegram", groups: map[string]tunnel.GroupStatus{"telegram": {Kind: "telegram", Route: "direct"}}, want: shared + direct},
			{name: "several", groups: map[string]tunnel.GroupStatus{"a": {Kind: "relay", Route: "direct", LastConn: &recent}, "b": {Kind: "telegram", Route: "tunnel", RouteID: "berry", LastConn: &recent}}, want: mail + direct + ", " + tg + "berry"},
			{name: "pinned", mode: "route:berry", groups: map[string]tunnel.GroupStatus{"mail": {Kind: "relay", Route: "tunnel", LastConn: &recent}}, want: mail + "berry"},
			{name: "type", groups: map[string]tunnel.GroupStatus{"mail": {Kind: "relay", Route: "tunnel", Via: "iroh", LastConn: &recent}}, want: mail + "iroh"},
		}
		for _, tc := range cases {
			t.Run(lang+"/"+tc.name, func(t *testing.T) {
				s := tunnel.Status{Mode: tc.mode, Route: "direct", Groups: tc.groups}
				got, _ := channelLines(tr, s, now, tc.shown)
				if got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			})
		}
	}
}

func TestChannelStability(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	d := channelDisplay{}
	s := tunnel.Status{Route: "direct"}
	tr := tr(table("en"))
	if got := d.sample(tr, s, now); got != "Route: direct" {
		t.Fatal(got)
	}
	s.Route = "smart"
	d.sample(tr, s, now.Add(time.Second))
	if got := d.sample(tr, s, now.Add(10*time.Second)); got != "Route: direct" {
		t.Fatal(got)
	}
	if got := d.sample(tr, s, now.Add(11*time.Second)); got != "Route: smart" {
		t.Fatal(got)
	}
	s.Route = "direct"
	d.sample(tr, s, now.Add(12*time.Second))
	s.Route = "tunnel"
	s.Via = "iroh"
	d.sample(tr, s, now.Add(15*time.Second))
	if got := d.sample(tr, s, now.Add(24*time.Second)); got != "Route: smart" {
		t.Fatal(got)
	}
	if got := d.sample(tr, s, now.Add(25*time.Second)); got != "Route: iroh" {
		t.Fatal(got)
	}
}

func TestStatusDetail(t *testing.T) {
	tr := tr(table("en"))
	if got, _ := channelLines(tr, tunnel.Status{}, time.Unix(2_000_000, 0), nil); got != "" {
		t.Fatalf("no route yet: got %q, want empty", got)
	}
	lines := "Mail: berry"
	for _, tc := range []struct{ state, lines, want string }{
		{"working", lines, lines},
		{"limited", lines, tr.f("h_reason_limited") + " " + lines},
		{"starting", lines, ""},
		{"problem", lines, ""},
		{"ready", lines, ""},
		{"working", "", ""},
	} {
		if got := statusDetail(tr, tunnel.Health{State: tc.state}, tc.lines); got != tc.want {
			t.Errorf("%s %q: got %q, want %q", tc.state, tc.lines, got, tc.want)
		}
	}
}
