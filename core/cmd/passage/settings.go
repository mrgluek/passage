package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mrgluek/passage/core/tunnel"
)

const (
	iniName  = "passage.ini"
	linkName = "link.txt"
	dataName = "data" // router state, iroh and WebView2 files
	host     = "127.0.0.1"
	// Below the ephemeral ranges of Linux (32768+) and Windows (49152+): a
	// client retrying a stopped proxy can't get the port as its source port.
	minPort = 20000
	maxPort = 32768 // exclusive
)

// Settings is what passage.ini keeps. The link is in link.txt.
type Settings struct {
	Port    int
	User    string
	Pass    string
	Auth    bool
	Mode    string
	My      string
	Running bool
}

func defaultSettings() Settings {
	s := Settings{Auth: true, Mode: tunnel.ModeAuto, Running: true}
	s.newAccess()
	return s
}

// newAccess picks a new port, username and password; apps set up with the
// old ones must be updated.
func (s *Settings) newAccess() {
	n, _ := rand.Int(rand.Reader, big.NewInt(maxPort-minPort))
	s.Port = minPort + int(n.Int64())
	s.User = randomHex(4)
	s.Pass = randomHex(16)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s Settings) listen() string { return fmt.Sprintf("%s:%d", host, s.Port) }

// proxyLink is the proxy as a tg://socks link, which some apps take in
// their proxy settings.
func (s Settings) proxyLink(scheme string) string {
	creds := ""
	if s.Auth {
		creds = "&user=" + s.User + "&pass=" + s.Pass
	}
	return fmt.Sprintf("%s?server=%s&port=%d%s", scheme, host, s.Port, creds)
}

// socksURL is the proxy as a socks5:// URL, which desktop apps take in
// their proxy settings.
func (s Settings) socksURL() string {
	creds := ""
	if s.Auth {
		creds = s.User + ":" + s.Pass + "@"
	}
	return fmt.Sprintf("socks5://%s%s:%d", creds, host, s.Port)
}

// loadSettings reads passage.ini; missing keys keep their defaults, and a
// missing port or credentials get new ones.
func loadSettings(path string) (Settings, bool, error) {
	s := Settings{Auth: true, Mode: tunnel.ModeAuto, Running: true}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return defaultSettings(), false, nil
	}
	if err != nil {
		return defaultSettings(), false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == ';' || line[0] == '#' || line[0] == '[' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "port":
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
				s.Port = n
			}
		case "user":
			s.User = v
		case "pass":
			s.Pass = v
		case "auth":
			s.Auth = v != "false" && v != "0"
		case "mode":
			s.Mode = v
		case "my_route":
			s.My = v
		case "running":
			s.Running = v != "false" && v != "0"
		}
	}
	fresh := defaultSettings()
	if s.Port == 0 {
		s.Port = fresh.Port
	}
	if s.User == "" || s.Pass == "" {
		s.User, s.Pass = fresh.User, fresh.Pass
	}
	if s.Mode == "" {
		s.Mode = tunnel.ModeAuto
	}
	return s, true, sc.Err()
}

func (s Settings) save(path string) error {
	text := fmt.Sprintf(`; Passage settings. Passage rewrites this file when you change them in its window.
[proxy]
port=%d
user=%s
pass=%s
auth=%t
mode=%s
my_route=%s
running=%t
`, s.Port, s.User, s.Pass, s.Auth, s.Mode, s.My, s.Running)
	return writeFile(path, []byte(text))
}

// writeFile replaces path through a temporary file, so a crash never
// leaves half a file.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// portableDir is where the settings live: next to the program, or in the
// user's config directory when that folder is read-only.
func portableDir(override string) (string, error) {
	if override != "" {
		dir, err := filepath.Abs(override)
		if err == nil {
			err = os.MkdirAll(dir, 0700)
		}
		return dir, err
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err == nil {
		dir := filepath.Dir(exe)
		if writable(dir) {
			return dir, nil
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "Passage")
	return dir, os.MkdirAll(dir, 0700)
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".passage-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}
