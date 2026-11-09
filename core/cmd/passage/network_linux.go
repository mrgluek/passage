package main

import (
	"encoding/binary"
	"net"
	"syscall"

	"github.com/mrgluek/passage/core/internal/sysdns"
)

func defaultRoutes() ([]networkRoute, error) {
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC)
	if err != nil {
		return nil, err
	}
	messages, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return nil, err
	}
	var routes []networkRoute
	for _, m := range messages {
		if m.Header.Type != syscall.RTM_NEWROUTE || len(m.Data) < syscall.SizeofRtMsg {
			continue
		}
		family, dstLen, table, kind := m.Data[0], m.Data[1], m.Data[4], m.Data[7]
		if dstLen != 0 || table != syscall.RT_TABLE_MAIN || kind != syscall.RTN_UNICAST || family != syscall.AF_INET && family != syscall.AF_INET6 {
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}
		index, metric, gateway := 0, uint64(0), ""
		for _, attr := range attrs {
			switch attr.Attr.Type {
			case syscall.RTA_OIF:
				if len(attr.Value) >= 4 {
					index = int(binary.NativeEndian.Uint32(attr.Value))
				}
			case syscall.RTA_PRIORITY:
				if len(attr.Value) >= 4 {
					metric = uint64(binary.NativeEndian.Uint32(attr.Value))
				}
			case syscall.RTA_GATEWAY:
				gateway = net.IP(attr.Value).String()
			}
		}
		if r, ok := interfaceRoute(index, gateway, metric, family == syscall.AF_INET6); ok {
			routes = append(routes, r)
		}
	}
	return routes, nil
}

func networkResolvers([]networkRoute) []string { return sysdns.Resolvers() }
