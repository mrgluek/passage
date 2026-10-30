// Package tunnel is Passage's router: a local SOCKS5 proxy that connects
// directly while the network is open and through a soar tunnel
// (github.com/getlantern/soar) when it is not.
//
// Nothing here names a server. The zone and key arrive in a link at runtime.
package tunnel

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Modes. Auto routes directly and switches to the tunnel when the probe
// hosts are blocked; Always forces the tunnel; Off never uses it.
const (
	ModeAuto   = "auto"
	ModeAlways = "always"
	ModeOff    = "off"
)

// DefaultResolvers are public resolvers used in addition to the network's
// and the link's. soar uses all of them and backs off the ones that don't
// answer. Port 853 means DNS-over-TLS.
var DefaultResolvers = []string{
	// NSDI.
	"195.208.4.1:53", "195.208.5.1:53", "[2a0c:a9c7:8::1]:53",
	// MSK-IX.
	"62.76.62.76:53", "62.76.76.62:53",
	// Yandex basic, Safe and Family.
	"77.88.8.8:53", "77.88.8.1:53", "[2a02:6b8::feed:0ff]:53", "[2a02:6b8:0:1::feed:0ff]:53",
	"77.88.8.88:53", "77.88.8.2:53", "[2a02:6b8::feed:bad]:53", "[2a02:6b8:0:1::feed:bad]:53",
	"77.88.8.7:53", "77.88.8.3:53", "[2a02:6b8::feed:a11]:53", "[2a02:6b8:0:1::feed:a11]:53",
	"77.88.8.8:853", "77.88.8.1:853", "77.88.8.88:853", "77.88.8.7:853",
	// TransTeleCom.
	"217.150.34.129:53",
	// SafeDNS, Google, Quad9.
	"195.46.39.39:53", "195.46.39.40:53",
	"8.8.8.8:53", "8.8.4.4:53",
	"9.9.9.9:53",
}

// DefaultCanaries tell "blocked" from "offline": one of them answering while
// every probe host fails means blocked.
var DefaultCanaries = []string{"ya.ru:443", "max.ru:443", "gosuslugi.ru:443"}

const maxResolvers = 64

// Config is the tunnel configuration. Zone, Key and Resolvers come from the
// link; the rest is local state the app keeps next to it.
type Config struct {
	Zone      string   `json:"zone"`
	Key       string   `json:"key"` // server Ed25519 public key, standard base64
	Resolvers []string `json:"resolvers"`
	Canaries  []string `json:"canaries,omitempty"`
	// ProbeTargets fixes the hosts the monitor TLS-probes to decide the route
	// (host:port, bare host = :443). Empty = learn them from the hostnames
	// that pass through the proxy.
	ProbeTargets []string `json:"probe_targets,omitempty"`
	// SystemResolvers are the DNS servers the phone's current network handed
	// out (the operator's on mobile data); Operator is the SIM's MCC+MNC.
	// Both come from the app at start, never from a link.
	SystemResolvers []string `json:"system_resolvers,omitempty"`
	Operator        string   `json:"operator,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	Listen          string   `json:"listen,omitempty"` // default 127.0.0.1:0
	User            string   `json:"user,omitempty"`   // SOCKS5 credentials; empty = no auth
	Pass            string   `json:"pass,omitempty"`
	// SafeQName is the query-name length every resolver path starts at; soar
	// grows it to the 253 maximum. 0 = soarSafeQName; 101 = soar's default.
	SafeQName int `json:"safe_qname,omitempty"`
	// StateFile keeps what the router learned across restarts (the smart
	// route's strategy per network), in the app's private storage.
	StateFile string `json:"state_file,omitempty"`
}

// LinkHost and LinkPath give the web form of links:
// https://passage.gluek.info/link#<fields>. The fields after # never reach
// the server; a path of its own keeps the site's other pages in the browser.
const (
	LinkHost = "passage.gluek.info"
	LinkPath = "/link"
)

// ParseLink parses passage://tunnel?v=1&zone=<zone>&key=<base64>[&r=<ip:port,…>][&c=<host:port,…>].
// The web form https://passage.gluek.info/link#v=1&zone=… and the older
// velta://tunnel form carry the same fields.
// r adds resolvers to the defaults; c replaces the default canaries.
// The key may be standard or URL-safe base64, padded or not.
func ParseLink(link string) (Config, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return Config{}, fmt.Errorf("bad link: %w", err)
	}
	var raw string
	switch scheme := strings.ToLower(u.Scheme); {
	case (scheme == "passage" || scheme == "velta") && strings.EqualFold(u.Host, "tunnel"):
		raw = u.RawQuery
	case scheme == "https" && strings.EqualFold(u.Host, LinkHost) && strings.TrimSuffix(u.Path, "/") == LinkPath:
		raw = u.EscapedFragment()
	default:
		return Config{}, errors.New("not a passage://tunnel link")
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return Config{}, fmt.Errorf("bad link: %w", err)
	}
	if v := q.Get("v"); v != "" && v != "1" {
		return Config{}, fmt.Errorf("unsupported link version %q", v)
	}
	cfg := Config{Zone: q.Get("zone"), Key: q.Get("key")}
	if r := q.Get("r"); r != "" {
		cfg.Resolvers = strings.Split(r, ",")
	}
	if c := q.Get("c"); c != "" {
		cfg.Canaries = strings.Split(c, ",")
	}
	n, err := cfg.Normalize()
	if err != nil {
		return Config{}, err
	}
	// Keep only what the link set: default canaries are filled at start, so
	// an app update with better ones reaches tunnels installed earlier.
	if len(cfg.Canaries) == 0 {
		n.Canaries = nil
	}
	return n, nil
}

// Normalize validates the config and fills defaults.
func (c Config) Normalize() (Config, error) {
	c.Zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.Zone)), ".")
	// Without a zone and key (no link yet) the router runs without the tunnel.
	if c.Zone != "" || strings.TrimSpace(c.Key) != "" {
		if err := checkZone(c.Zone); err != nil {
			return Config{}, err
		}
		key, err := decodeKey(c.Key)
		if err != nil {
			return Config{}, err
		}
		c.Key = base64.StdEncoding.EncodeToString(key)
	}

	var rs []string
	seen := map[string]bool{}
	for _, r := range c.Resolvers {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		norm, err := checkResolver(r)
		if err != nil {
			return Config{}, err
		}
		if !seen[norm] {
			seen[norm] = true
			rs = append(rs, norm)
		}
	}
	if len(rs) > maxResolvers {
		return Config{}, fmt.Errorf("too many resolvers (%d, max %d)", len(rs), maxResolvers)
	}
	// Link resolvers add to DefaultResolvers (see Effective), so better
	// defaults in an app update reach every installed tunnel.
	c.Resolvers = rs

	// The network's own resolvers go first. Bad entries from the device are
	// dropped, not fatal.
	var sys []string
	for _, r := range c.SystemResolvers {
		if norm, err := checkResolver(strings.TrimSpace(r)); err == nil {
			sys = append(sys, norm)
		}
	}
	c.SystemResolvers = sys

	cs, err := hostPorts(c.Canaries, "canaries")
	if err != nil {
		return Config{}, err
	}
	if len(cs) == 0 {
		cs = append([]string(nil), DefaultCanaries...)
	}
	c.Canaries = cs
	if c.ProbeTargets, err = hostPorts(c.ProbeTargets, "probe targets"); err != nil {
		return Config{}, err
	}
	switch c.Mode {
	case "":
		c.Mode = ModeAuto
	case ModeAuto, ModeAlways, ModeOff:
	default:
		return Config{}, fmt.Errorf("unknown mode %q", c.Mode)
	}
	if c.Mode == ModeAlways && !c.HasTunnel() {
		c.Mode = ModeAuto
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:0"
	}
	return c, nil
}

// HasTunnel reports whether the config carries a tunnel (a link was given).
func (c Config) HasTunnel() bool { return c.Zone != "" }

// Effective is the resolver list the tunnel uses: the network's resolvers,
// then the SIM operator's known resolvers, then the link's extra resolvers,
// then DefaultResolvers, without duplicates and capped at maxResolvers.
func (c Config) Effective() []string {
	seen := map[string]bool{}
	var out []string
	add := func(list []string) {
		for _, r := range list {
			if !seen[r] && len(out) < maxResolvers {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	add(c.SystemResolvers)
	add(OperatorResolvers[c.Operator])
	add(c.Resolvers)
	add(DefaultResolvers)
	return out
}

// hostPorts trims list entries, drops empty ones and gives a bare host port 443.
func hostPorts(list []string, what string) ([]string, error) {
	var out []string
	for _, h := range list {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(h); err != nil {
			h = net.JoinHostPort(h, "443")
		}
		out = append(out, h)
	}
	if len(out) > 8 {
		return nil, fmt.Errorf("too many %s (%d, max 8)", what, len(out))
	}
	return out, nil
}

func checkZone(z string) error {
	if len(z) == 0 || len(z) > 200 {
		return errors.New("bad zone")
	}
	labels := strings.Split(z, ".")
	if len(labels) < 2 {
		return errors.New("zone needs at least two labels")
	}
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("bad zone label %q", l)
		}
		for _, ch := range l {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return fmt.Errorf("bad zone label %q", l)
			}
		}
	}
	return nil
}

func decodeKey(s string) ([]byte, error) {
	// A '+' of standard base64 arrives as a space after query unescaping, so
	// spaces become '+' again and only line breaks and tabs are trimmed.
	s = strings.ReplaceAll(strings.Trim(s, "\t\r\n"), " ", "+")
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != 32 {
				return nil, fmt.Errorf("key must be 32 bytes, got %d", len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("key is not base64")
}

// checkResolver accepts only IP literals: an app on Android may have no usable
// system resolver for a hostname, and a resolver named by DNS defeats the point.
func checkResolver(r string) (string, error) {
	host, port, err := net.SplitHostPort(r)
	if err != nil {
		// Bare IP: default to port 53.
		if ip := net.ParseIP(strings.Trim(r, "[]")); ip != nil {
			return net.JoinHostPort(ip.String(), "53"), nil
		}
		return "", fmt.Errorf("bad resolver %q", r)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("resolver %q must be an IP address", r)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("bad resolver port in %q", r)
	}
	return net.JoinHostPort(ip.String(), port), nil
}
