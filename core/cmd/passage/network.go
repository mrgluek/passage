package main

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/mrgluek/passage/core/internal/sysdns"
)

const networkSettle = 3 * time.Second

type networkRoute struct {
	name, gateway, address string
	index                  int
	metric                 uint64
	ipv6                   bool
}

type networkState struct {
	dns   []string
	paths string
}

func (s networkState) key() string {
	dns := slices.Clone(s.dns)
	slices.Sort(dns)
	return strings.Join(slices.Compact(dns), ",") + "|" + s.paths
}

type networkChange struct {
	current, pending string
	since            time.Time
}

func (n *networkChange) observe(key string, now time.Time) bool {
	if key == n.current {
		n.pending, n.since = "", time.Time{}
		return false
	}
	if key != n.pending {
		n.pending, n.since = key, now
		return false
	}
	if now.Sub(n.since) < networkSettle {
		return false
	}
	n.current, n.pending, n.since = key, "", time.Time{}
	return true
}

func physicalInterfaceName(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"loopback", "utun", "tun", "tap", "wg", "tailscale", "docker", "br-", "veth", "virbr", "vmnet", "vbox", "bridge", "vethernet"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func interfaceRoute(index int, gateway string, metric uint64, ipv6 bool) (networkRoute, bool) {
	iface, err := net.InterfaceByIndex(index)
	if err != nil {
		return networkRoute{}, false
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return networkRoute{}, false
	}
	return interfaceRouteFor(*iface, addresses, gateway, metric, ipv6)
}

func interfaceRouteFor(iface net.Interface, addresses []net.Addr, gateway string, metric uint64, ipv6 bool) (networkRoute, bool) {
	if iface.Flags&net.FlagUp == 0 || iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 || !physicalInterfaceName(iface.Name) {
		return networkRoute{}, false
	}
	var ips []string
	for _, addr := range addresses {
		ip, _, err := net.ParseCIDR(addr.String())
		if err == nil && ip.IsGlobalUnicast() && (ip.To4() == nil) == ipv6 {
			address := ip.String()
			if ipv6 {
				address = ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
			}
			ips = append(ips, address)
		}
	}
	slices.Sort(ips)
	address := ""
	if len(ips) > 0 {
		address = ips[0]
	}
	return networkRoute{name: iface.Name, index: iface.Index, gateway: gateway, address: address, metric: metric, ipv6: ipv6}, true
}

func primaryRoutes(routes []networkRoute) []networkRoute {
	var out []networkRoute
	for _, ipv6 := range []bool{false, true} {
		var best *networkRoute
		for i := range routes {
			r := &routes[i]
			if r.ipv6 != ipv6 || !physicalInterfaceName(r.name) {
				continue
			}
			if best == nil || r.metric < best.metric || r.metric == best.metric && (r.index < best.index || r.index == best.index && routeKey(*r) < routeKey(*best)) {
				best = r
			}
		}
		if best != nil {
			out = append(out, *best)
		}
	}
	return out
}

func routeKey(r networkRoute) string {
	return fmt.Sprintf("%t/%d/%s/%s", r.ipv6, r.index, r.gateway, r.address)
}

func readNetwork() (networkState, bool) {
	return readNetworkWith(defaultRoutes, networkResolvers, sysdns.Resolvers)
}

func readNetworkWith(readRoutes func() ([]networkRoute, error), resolvers func([]networkRoute) []string, fallback func() []string) (networkState, bool) {
	routes, err := readRoutes()
	if err != nil {
		return networkStateFor(fallback(), nil), false
	}
	routes = primaryRoutes(routes)
	dns := resolvers(routes)
	if len(dns) == 0 {
		dns = fallback()
	}
	return networkStateFor(dns, routes), true
}

func networkStateFor(dns []string, routes []networkRoute) networkState {
	var keys []string
	for _, r := range primaryRoutes(routes) {
		keys = append(keys, routeKey(r))
	}
	return networkState{dns: dns, paths: strings.Join(keys, ",")}
}

func (a *App) networkChanged(now time.Time) bool {
	state, ok := readNetwork()
	return a.network.observeState(state, ok, now)
}

func (n *networkChange) observeState(state networkState, ok bool, now time.Time) bool {
	if !ok {
		// A route-read failure leaves only the resolver set to compare.
		_, state.paths, _ = strings.Cut(n.current, "|")
	}
	return n.observe(state.key(), now)
}
