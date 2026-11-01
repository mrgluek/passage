package link

import (
	"encoding/base32"
	"encoding/binary"
	"errors"
	"net"
	"net/url"
	"strings"
)

// validateTicket accepts iroh 0.35 variant-0 tickets with a relay and no direct addresses.
func validateTicket(ticket, relay string) error {
	if !strings.HasPrefix(ticket, "node") || len(ticket) > 4096 {
		return errors.New("invalid endpoint ticket")
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(ticket[4:]))
	if err != nil || len(raw) < 36 || raw[0] != 0 || raw[33] != 1 {
		return errors.New("invalid endpoint ticket")
	}
	size, n := binary.Uvarint(raw[34:])
	if n <= 0 || size > 2048 {
		return errors.New("invalid ticket relay")
	}
	start := 34 + n
	if uint64(len(raw)-start) != size+1 || raw[len(raw)-1] != 0 {
		return errors.New("ticket must contain a relay and no direct addresses")
	}
	actual := string(raw[start : start+int(size)])
	a, err := relayOrigin(actual)
	if err != nil {
		return err
	}
	b, err := relayOrigin(relay)
	if err != nil {
		return err
	}
	if a != b {
		return errors.New("ticket relay does not match route relay")
	}
	return nil
}
func relayOrigin(text string) (string, error) {
	u, err := url.Parse(text)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || (u.Port() != "" && u.Port() != "443") {
		return "", errors.New("invalid relay URL")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(host, "443"), nil
}
