package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"time"
)

// interceptProbeTargets are documentation addresses (RFC 5737 TEST-NET-1/2):
// no DNS server lives there, so an answer means the network redirects all
// port-53 traffic to a resolver of its choosing.
var interceptProbeTargets = []string{"192.0.2.1:53", "198.51.100.1:53"}

// detectInterception reports whether any probe target answers within timeout.
func detectInterception(ctx context.Context, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	found := make(chan bool, len(interceptProbeTargets))
	for _, target := range interceptProbeTargets {
		go func(target string) {
			_, err := dnsExchangeA(ctx, target, "example.com")
			found <- err == nil
		}(target)
	}
	for range interceptProbeTargets {
		if <-found {
			return true
		}
	}
	return false
}

// dnsWhoami asks resolver for whoami.akamai.net, whose A record is the
// address of the resolver that actually queried Akamai: on an intercepting
// network, the interceptor's upstream.
func dnsWhoami(ctx context.Context, resolver string) (net.IP, error) {
	ips, err := dnsExchangeA(ctx, resolver, "whoami.akamai.net")
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("no A record")
	}
	return ips[0], nil
}

// dnsExchangeA sends one A query over UDP and returns the A records of a
// matching response. Any well-formed response counts as an answer.
func dnsExchangeA(ctx context.Context, server, name string) ([]net.IP, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	var idb [2]byte
	rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	q, err := buildQueryA(id, name)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if ips, err := parseResponseA(buf[:n], id); err == nil {
			return ips, nil
		}
	}
}

func buildQueryA(id uint16, name string) ([]byte, error) {
	b := make([]byte, 12, 12+len(name)+6)
	binary.BigEndian.PutUint16(b[0:], id)
	b[2] = 0x01                          // RD
	binary.BigEndian.PutUint16(b[4:], 1) // QDCOUNT
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, errors.New("bad name")
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0, 0, 1, 0, 1) // root, QTYPE A, QCLASS IN
	return b, nil
}

// parseResponseA validates the header against id and returns the A records.
func parseResponseA(b []byte, id uint16) ([]net.IP, error) {
	if len(b) < 12 || binary.BigEndian.Uint16(b[0:]) != id || b[2]&0x80 == 0 {
		return nil, errors.New("not a response to our query")
	}
	qd := int(binary.BigEndian.Uint16(b[4:]))
	an := int(binary.BigEndian.Uint16(b[6:]))
	off := 12
	var err error
	for i := 0; i < qd; i++ {
		if off, err = skipName(b, off); err != nil {
			return nil, err
		}
		off += 4
	}
	var ips []net.IP
	for i := 0; i < an; i++ {
		if off, err = skipName(b, off); err != nil {
			return nil, err
		}
		if off+10 > len(b) {
			return nil, errors.New("truncated answer")
		}
		typ := binary.BigEndian.Uint16(b[off:])
		rdlen := int(binary.BigEndian.Uint16(b[off+8:]))
		off += 10
		if off+rdlen > len(b) {
			return nil, errors.New("truncated rdata")
		}
		if typ == 1 && rdlen == 4 {
			ips = append(ips, net.IPv4(b[off], b[off+1], b[off+2], b[off+3]))
		}
		off += rdlen
	}
	return ips, nil
}

func skipName(b []byte, off int) (int, error) {
	for {
		if off >= len(b) {
			return 0, errors.New("truncated name")
		}
		l := int(b[off])
		switch {
		case l == 0:
			return off + 1, nil
		case l&0xC0 == 0xC0: // compression pointer ends the name here
			return off + 2, nil
		default:
			off += 1 + l
		}
	}
}

// dotFirst moves DNS-over-TLS (port 853) resolvers to the front, keeping the
// order otherwise.
func dotFirst(resolvers []string) []string {
	var dot, rest []string
	for _, r := range resolvers {
		if isDoT(r) {
			dot = append(dot, r)
		} else {
			rest = append(rest, r)
		}
	}
	return append(dot, rest...)
}
