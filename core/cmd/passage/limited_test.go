package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

func TestLimitedLine(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		tr := tr(table(lang))
		prefix := tr.f("net_limited")
		if prefix == "" || prefix == "net_limited" {
			t.Fatalf("%s: prefix missing: %q", lang, prefix)
		}
		for _, tc := range []struct {
			limited bool
			line    string
			want    string
		}{
			{false, "", ""},
			{false, "Mail: berry", "Mail: berry"},
			{true, "", prefix},
			{true, "Mail: berry", prefix + " · Mail: berry"},
		} {
			if got := limitedLine(tr, tc.limited, tc.line); got != tc.want {
				t.Errorf("%s %v %q: got %q, want %q", lang, tc.limited, tc.line, got, tc.want)
			}
		}
		// The prefix never reaches a title or the window caption.
		for key, v := range table(lang) {
			if strings.HasPrefix(key, "h_title_") && strings.Contains(v, prefix) {
				t.Errorf("%s: title %s contains the prefix", lang, key)
			}
		}
		h := tunnel.Health{State: "working"}
		if got := limitedLine(tr, true, statusDetail(tr, h, "Mail: berry")); got != prefix+" · Mail: berry" {
			t.Errorf("%s: detail %q", lang, got)
		}
	}
}

func TestFullStatusCanaryText(t *testing.T) {
	tr := tr(table("en"))
	s := tunnel.Status{LastProbe: time.Unix(2_000_000, 0)}
	base := fullStatus(tr, s)
	bad, ok := tr.f("status_canary_bad"), tr.f("status_canary_ok")
	if strings.Contains(base, bad) || strings.Contains(base, ok) {
		t.Fatalf("canary text without a canary check:\n%s", base)
	}
	s.LastResult.CanaryOK = true
	if got := fullStatus(tr, s); got != base {
		t.Fatalf("canary_ok alone changed the status:\n%s", got)
	}
	s.LastResult.CanaryChecked = true
	if got := fullStatus(tr, s); !strings.Contains(got, ok) {
		t.Fatalf("checked canary not shown:\n%s", got)
	}
}

func TestUIScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	html, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllSubmatch(html, -1)
	if len(blocks) == 0 {
		t.Fatal("no script block")
	}
	for i, b := range blocks {
		path := filepath.Join(t.TempDir(), "ui.js")
		if err := os.WriteFile(path, b[1], 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
			t.Fatalf("script %d does not parse: %v\n%s", i, err, out)
		}
	}
}

func TestFitTooltip(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		tr := tr(table(lang))
		title := "Passage: " + tr.f("h_title_working")
		detail := "Mail: berry-one-two, Messenger: cherry-three-four, Calls: apple-five-six-seven-eight, Files: plum-nine-ten-eleven"
		got := fitTooltip(title+"\n"+limitedLine(tr, true, detail), maxTooltip)
		if n := len([]rune(got)); n > maxTooltip {
			t.Errorf("%s: %d runes", lang, n)
		}
		if !strings.HasPrefix(got, title+"\n"+tr.f("net_limited")) {
			t.Errorf("%s: title or prefix lost: %q", lang, got)
		}
		if strings.HasSuffix(got, " ") || strings.HasSuffix(got, ",") || strings.HasSuffix(got, "·") || !strings.HasSuffix(got, "…") {
			t.Errorf("%s: bad cut: %q", lang, got)
		}
		// The cut is at a word boundary: what remains is whole words of the input.
		body := strings.TrimSuffix(got, "…")
		if !strings.HasPrefix(title+"\n"+limitedLine(tr, true, detail), body) {
			t.Errorf("%s: not a prefix: %q", lang, got)
		}
		rest := strings.TrimPrefix(title+"\n"+limitedLine(tr, true, detail), body)
		if rest != "" && !strings.ContainsRune(" ,;\n", []rune(rest)[0]) {
			t.Errorf("%s: cut inside a word: %q", lang, got)
		}
	}
	if got := fitTooltip("short\ndetail", maxTooltip); got != "short\ndetail" {
		t.Errorf("short tooltip changed: %q", got)
	}
}
