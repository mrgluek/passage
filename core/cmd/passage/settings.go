package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	links "github.com/mrgluek/passage-link/link"
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
	Port         int
	User         string
	Pass         string
	Auth         bool
	Mode         string
	My           string
	Running      bool
	AutoUpdate   bool
	NextUpdate   time.Time
	RoutesNotice string
	// Edits are local changes to the link's routes, by route ID.
	Edits map[string]tunnel.RouteEdit
}

func defaultSettings() Settings {
	s := Settings{Auth: true, Mode: tunnel.ModeAuto, Running: true, AutoUpdate: true}
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
	s := Settings{Auth: true, Mode: tunnel.ModeAuto, Running: true, AutoUpdate: true}
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
		if id, ok := strings.CutPrefix(k, "route."); ok && links.ValidID(id) {
			if e, ok := parseRouteEdit(v); ok {
				if s.Edits == nil {
					s.Edits = map[string]tunnel.RouteEdit{}
				}
				s.Edits[id] = e
			}
			continue
		}
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
		case "auto_update":
			s.AutoUpdate = v != "false" && v != "0"
		case "routes_notice":
			s.RoutesNotice = v
		case "next_update":
			s.NextUpdate, _ = time.Parse(time.RFC3339, v)
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

// parseRouteEdit reads "20", "off" or "20,off".
func parseRouteEdit(v string) (tunnel.RouteEdit, bool) {
	var e tunnel.RouteEdit
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "off" {
			e.Off = true
		} else if n, err := strconv.ParseUint(part, 10, 16); err == nil {
			p := uint16(n)
			e.Priority = &p
		} else if part != "" {
			return e, false
		}
	}
	return e, e.Off || e.Priority != nil
}

// editLines is the route edits as passage.ini lines, in ID order.
func (s Settings) editLines() string {
	ids := make([]string, 0, len(s.Edits))
	for id := range s.Edits {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var b strings.Builder
	for _, id := range ids {
		e := s.Edits[id]
		var parts []string
		if e.Priority != nil {
			parts = append(parts, strconv.Itoa(int(*e.Priority)))
		}
		if e.Off {
			parts = append(parts, "off")
		}
		fmt.Fprintf(&b, "route.%s=%s\n", id, strings.Join(parts, ","))
	}
	return b.String()
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
auto_update=%t
next_update=%s
routes_notice=%s
`, s.Port, s.User, s.Pass, s.Auth, s.Mode, s.My, s.Running, s.AutoUpdate, s.NextUpdate.Format(time.RFC3339), s.RoutesNotice)
	text += s.editLines()
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

// portableDir uses Application Support on macOS. Elsewhere it prefers
// the program's folder, falling back to the user's config directory.
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
	if err == nil && runtime.GOOS != "darwin" {
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
