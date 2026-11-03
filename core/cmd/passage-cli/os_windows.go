package main

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

const exeSuffix = ".exe"

// adapterResolvers lists the DNS servers of the network adapters that are up.
func adapterResolvers() []string {
	size := uint32(15000)
	for range 3 {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return nil
		}
		var out []string
		seen := map[string]bool{}
		for a := first; a != nil; a = a.Next {
			if a.OperStatus != windows.IfOperStatusUp || a.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
				continue
			}
			for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
				ip := d.Address.IP()
				// fec0::/10 site-local defaults are what Windows lists with no IPv6 DNS.
				if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.To4() == nil && ip[0] == 0xfe && ip[1]&0xc0 == 0xc0 {
					continue
				}
				if hp := net.JoinHostPort(ip.String(), "53"); !seen[hp] {
					seen[hp] = true
					out = append(out, hp)
				}
			}
		}
		return out
	}
	return nil
}
