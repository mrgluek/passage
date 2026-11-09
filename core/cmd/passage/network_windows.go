package main

import (
	"net"
	"unsafe"

	"github.com/mrgluek/passage/core/internal/sysdns"
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

func networkResolvers([]networkRoute) []string { return sysdns.Resolvers() }
