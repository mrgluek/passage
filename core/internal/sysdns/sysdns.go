// Package sysdns lists this machine's DNS servers.
package sysdns

import (
	"bufio"
	"net"
	"os"
	"strings"
)

// Resolvers reads this machine's DNS servers: from the network
// adapters on Windows, from resolv.conf elsewhere. With systemd-resolved,
// /etc/resolv.conf only names the 127.0.0.53 stub, so the real upstream list
// comes from /run/systemd/resolve/resolv.conf. Loopback entries are skipped:
// a local stub can't forward to the tunnel zone any better than its upstreams.
func Resolvers() []string {
	if out := adapterResolvers(); out != nil {
		return out
	}
	for _, path := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var out []string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 || fields[0] != "nameserver" {
				continue
			}
			ip := net.ParseIP(strings.SplitN(fields[1], "%", 2)[0])
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, net.JoinHostPort(ip.String(), "53"))
		}
		f.Close()
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
