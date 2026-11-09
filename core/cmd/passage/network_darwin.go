package main

import (
	"encoding/binary"
	"net"
	"syscall"

	"github.com/mrgluek/passage/core/internal/sysdns"
)

func defaultRoutes() ([]networkRoute, error) {
	rib, err := syscall.RouteRIB(syscall.NET_RT_DUMP, 0)
	if err != nil {
		return nil, err
	}
	messages, err := parseRouteMessages(rib)
	if err != nil {
		return nil, err
	}
	var routes []networkRoute
	for _, m := range messages {
		if m.Header.Flags&syscall.RTF_UP == 0 || m.Header.Flags&(syscall.RTF_REJECT|syscall.RTF_BLACKHOLE|syscall.RTF_HOST) != 0 {
			continue
		}
		addrs, err := syscall.ParseRoutingSockaddr(m)
		if err != nil {
			continue
		}
		dst, gateway := routeIP(addrs[syscall.RTAX_DST]), routeIP(addrs[syscall.RTAX_GATEWAY])
		if dst == nil || !dst.IsUnspecified() || gateway == nil {
			continue
		}
		mask := routeIP(addrs[syscall.RTAX_NETMASK])
		if mask != nil && !mask.IsUnspecified() {
			continue
		}
		metric := uint64(0)
		if m.Header.Flags&syscall.RTF_IFSCOPE != 0 {
			metric = 1
		}
		if r, ok := interfaceRoute(int(m.Header.Index), gateway.String(), metric, dst.To4() == nil); ok {
			routes = append(routes, r)
		}
	}
	return routes, nil
}

func routeIP(sa syscall.Sockaddr) net.IP {
	switch sa := sa.(type) {
	case *syscall.SockaddrInet4:
		return net.IP(sa.Addr[:])
	case *syscall.SockaddrInet6:
		return net.IP(sa.Addr[:])
	default:
		return nil
	}
}

func networkResolvers([]networkRoute) []string { return sysdns.Resolvers() }

func parseRouteMessages(rib []byte) ([]*syscall.RouteMessage, error) {
	var out []*syscall.RouteMessage
	for len(rib) > 0 {
		if len(rib) < 4 {
			return nil, syscall.EINVAL
		}
		size := int(binary.NativeEndian.Uint16(rib[:2]))
		if size < 4 || size > len(rib) {
			return nil, syscall.EINVAL
		}
		data := rib[:size]
		rib = rib[size:]
		if data[2] != syscall.RTM_VERSION || data[3] != syscall.RTM_GET {
			continue
		}
		var header syscall.RtMsghdr
		// Decode without assuming the kernel's 4-byte alignment.
		if _, err := binary.Decode(data, binary.NativeEndian, &header); err != nil {
			return nil, err
		}
		out = append(out, &syscall.RouteMessage{Header: header, Data: data[syscall.SizeofRtMsghdr:]})
	}
	return out, nil
}
