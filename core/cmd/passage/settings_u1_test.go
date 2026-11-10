package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVeltaLink(t *testing.T) {
	s := Settings{Auth: true, Port: 23456, User: "foobar", Pass: "f"}
	got := s.veltaLink()
	want := "velta://proxy/2/127.0.0.1/23456/U/MZXW6YTBOI/W/MY/N/" + "KBQXG43BM5SQ"
	if got != want {
		t.Fatalf("veltaLink = %q, want %q", got, want)
	}
	s.Auth = false
	if got := s.veltaLink(); strings.Contains(got, "/U/") || strings.Contains(got, "/W/") {
		t.Fatalf("link without credentials has user or password: %q", got)
	}
	s.Auth, s.User, s.Pass = true, "u", ""
	if got := s.veltaLink(); strings.Contains(got, "/W/") {
		t.Fatalf("empty password must be left out: %q", got)
	}
	if len(got) > 2048 || strings.HasSuffix(got, "/") || strings.ContainsAny(got, "?#=") {
		t.Fatalf("link breaks the format rules: %q", got)
	}
}

func TestAppSeenAndLanguagePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passage.ini")
	s := defaultSettings()
	s.AppSeen, s.Lang = true, "ru"
	if err := s.save(path); err != nil {
		t.Fatal(err)
	}
	back, _, err := loadSettings(path)
	if err != nil || !back.AppSeen || back.Lang != "ru" {
		t.Fatalf("loaded %+v, %v", back, err)
	}
	s.Lang = "xx"
	s.save(path)
	if back, _, _ := loadSettings(path); back.Lang != "" {
		t.Fatalf("unknown language kept: %q", back.Lang)
	}
}

func TestOldSettingsCountRunningProxyAsUsed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passage.ini")
	os.WriteFile(path, []byte("[proxy]\nport=23456\nuser=a\npass=b\nrunning=true\n"), 0600)
	if s, _, _ := loadSettings(path); !s.AppSeen {
		t.Fatal("a proxy that was running before the key existed should count as used")
	}
	os.WriteFile(path, []byte("[proxy]\nport=23456\nuser=a\npass=b\nrunning=false\n"), 0600)
	if s, _, _ := loadSettings(path); s.AppSeen {
		t.Fatal("a stopped proxy should not count as used")
	}
}

func TestSetLanguage(t *testing.T) {
	a := newApp(t.TempDir(), "test", "", "en", &fakePlatform{})
	if err := a.setLanguage("ru"); err != nil || a.lang != "ru" || a.t["h_help"] != "Что-то не так?" {
		t.Fatalf("lang %q err %v", a.lang, err)
	}
	if err := a.setLanguage(""); err != nil || a.lang != "en" {
		t.Fatalf("system language not restored: %q %v", a.lang, err)
	}
	if err := a.setLanguage("de"); err == nil {
		t.Fatal("unknown language accepted")
	}
}
