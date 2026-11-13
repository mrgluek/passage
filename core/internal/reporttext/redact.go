package reporttext

import (
	"net"
	"net/netip"
	"regexp"
	"strings"
)

const Address = "[addr]"

var (
	link          = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
	secret        = regexp.MustCompile(`(?i)(\b(?:read_key|signer|key|pass(?:word)?|user(?:name)?|ticket|zone|link|secret|credentials?|token|endpoint|dial_ip|sni|sources)"?[ \t]*[:=][ \t]*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\[[^\]\r\n]*\]|[^,\r\n}\]"\x60]+)`)
	localEndpoint = regexp.MustCompile(`(?:\[[0-9a-fA-F:.]+(?:%[^\]\s]+)?\]|[0-9]+(?:\.[0-9]+){3}):[0-9]+[ \t]*->`)
	address       = regexp.MustCompile(`(?:->[ \t]*)?(?:\[[0-9a-fA-F:.]+(?:%[^\]\s]+)?\](?::[0-9]+)?|[0-9]+(?:\.[0-9]+){3}(?::[0-9]+)?|[0-9a-fA-F]*:[0-9a-fA-F:.]+(?:%[\w.-]+)?)`)
)

func LocalEndpoints(s string) string {
	return localEndpoint.ReplaceAllString(s, Address+"->")
}

func Secrets(s string) string {
	s = secret.ReplaceAllString(s, "${1}[redacted]")
	return link.ReplaceAllString(s, "[link]")
}

// Addresses masks local socket endpoints and addresses selected by match.
// With no matcher it masks bare addresses and keeps named remote endpoints.
func Addresses(s string, match func(netip.Addr) bool) string {
	s = LocalEndpoints(s)
	var out strings.Builder
	last := 0
	for _, span := range address.FindAllStringIndex(s, -1) {
		start, end := span[0], span[1]
		token := s[start:end]
		remote := strings.HasPrefix(token, "->")
		if !remote && start > 0 && (addressWord(s[start-1]) || s[start-1] == '.') ||
			end < len(s) && (addressWord(s[end]) || s[end] == '.' && end+1 < len(s) && addressWord(s[end+1])) {
			continue
		}
		host := strings.TrimSpace(strings.TrimPrefix(token, "->"))
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host, suffix := strings.Trim(host, "[]"), ""
		trimmed := strings.TrimRight(host, ".")
		dots := host[len(trimmed):]
		host = trimmed
		ip, err := netip.ParseAddr(host)
		if err != nil && strings.HasSuffix(host, ":") {
			ip, err = netip.ParseAddr(strings.TrimSuffix(host, ":"))
			suffix = ":"
		}
		if err != nil || match == nil && remote || match != nil && !match(ip.WithZone("").Unmap()) {
			continue
		}
		out.WriteString(s[last:start])
		if remote {
			out.WriteString("->")
		}
		out.WriteString(Address + suffix + dots)
		last = end
	}
	if last == 0 {
		return s
	}
	out.WriteString(s[last:])
	return out.String()
}

func addressWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func Redact(s string) string {
	return Addresses(Secrets(s), nil)
}
