package main

import (
	"net"
	"net/netip"
	"slices"

	"github.com/mrgluek/passage/core/internal/reporttext"
)

func reportPrefixes() []netip.Prefix {
	addresses, _ := net.InterfaceAddrs()
	var prefixes []netip.Prefix
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil || prefix.Addr().IsLoopback() {
			continue
		}
		if prefix.Addr().Is4() {
			prefix = netip.PrefixFrom(prefix.Addr(), 32)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func maskReportAddresses(report map[string]any, prefixes []netip.Prefix) {
	if egress, _ := report["dns_egress"].(string); egress != "" {
		report["dns_egress"] = reporttext.Address
	}
	match := func(ip netip.Addr) bool {
		return slices.ContainsFunc(prefixes, func(prefix netip.Prefix) bool { return prefix.Contains(ip) })
	}
	var mask func(any) any
	mask = func(value any) any {
		switch v := value.(type) {
		case string:
			return reporttext.Addresses(v, match)
		case []string:
			for i, item := range v {
				v[i] = reporttext.Addresses(item, match)
			}
		case []any:
			for i, item := range v {
				v[i] = mask(item)
			}
		case map[string]any:
			for key, item := range v {
				if key == "msg" {
					if message, ok := item.(string); ok {
						item = reporttext.Secrets(message)
					}
				}
				v[key] = mask(item)
			}
		}
		return value
	}
	for key, value := range report {
		if key != "device_network" {
			report[key] = mask(value)
		}
	}
}
