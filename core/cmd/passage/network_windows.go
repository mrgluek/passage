package main

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

func defaultRoutes() ([]networkRoute, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_UNSPEC, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	var routes []networkRoute
	for _, row := range table.Rows() {
		if row.DestinationPrefix.PrefixLength != 0 || row.Loopback != 0 {
			continue
		}
		iface := windows.MibIfRow2{InterfaceIndex: row.InterfaceIndex}
		// HardwareInterface is bit zero; virtual adapters have it cleared.
		if windows.GetIfEntry2Ex(0, &iface) != nil || iface.InterfaceAndOperStatusFlags&1 == 0 {
			continue
		}
		ipif := windows.MibIpInterfaceRow{Family: row.DestinationPrefix.Prefix.Family, InterfaceIndex: row.InterfaceIndex}
		if windows.GetIpInterfaceEntry(&ipif) != nil {
			continue
		}
		gateway := ""
		switch row.NextHop.Family {
		case windows.AF_INET:
			sa := (*windows.RawSockaddrInet4)(unsafe.Pointer(&row.NextHop))
			gateway = net.IP(sa.Addr[:]).String()
		case windows.AF_INET6:
			sa := (*windows.RawSockaddrInet6)(unsafe.Pointer(&row.NextHop))
			gateway = net.IP(sa.Addr[:]).String()
		default:
			continue
		}
		metric := uint64(row.Metric) + uint64(ipif.Metric)
		if r, ok := interfaceRoute(int(row.InterfaceIndex), gateway, metric, ipif.Family == windows.AF_INET6); ok {
			routes = append(routes, r)
		}
	}
	return routes, nil
}

func networkResolvers(routes []networkRoute) []string {
	indices := map[uint32]bool{}
	for _, r := range routes {
		indices[uint32(r.index)] = true
	}
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
		for a := first; a != nil; a = a.Next {
			if !indices[a.IfIndex] && !indices[a.Ipv6IfIndex] {
				continue
			}
			for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
				ip := d.Address.IP()
				if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.To4() == nil && ip[0] == 0xfe && ip[1]&0xc0 == 0xc0 {
					continue
				}
				out = append(out, net.JoinHostPort(ip.String(), "53"))
			}
		}
		return out
	}
	return nil
}
