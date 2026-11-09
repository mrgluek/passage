package main

import (
	"errors"
	"net"
	"slices"
	"testing"
	"time"
)

func TestNetworkChangeDebounceRecordedEvents(t *testing.T) {
	base := networkRoute{name: "Ethernet", index: 1, gateway: "gateway-a", address: "address-a", metric: 10}
	address := base
	address.address = "address-b"
	gateway := address
	gateway.gateway = "gateway-b"
	changedAgain := gateway
	changedAgain.address = "address-c"
	metric := base
	metric.metric = 20
	virtual := networkRoute{name: "vEthernet (test)", index: 2, gateway: "virtual-gateway", address: "virtual-address"}
	loopback := virtual
	loopback.name = "Loopback"
	dns := []string{"a.example.org:53", "b.example.org:53"}
	reversed := []string{dns[1], dns[0]}
	changedDNS := []string{"c.example.org:53"}
	events := []struct {
		second  int
		name    string
		dns     []string
		routes  []networkRoute
		restart bool
	}{
		{0, "initial", dns, []networkRoute{base}, false},
		{1, "DNS order", reversed, []networkRoute{base}, false},
		{2, "duplicate DNS", append(slices.Clone(dns), dns[0]), []networkRoute{base}, false},
		{3, "virtual adapters", dns, []networkRoute{base, virtual, loopback}, false},
		{4, "metric only", dns, []networkRoute{metric}, false},
		{5, "transient address", dns, []networkRoute{address}, false},
		{6, "address restored", dns, []networkRoute{base}, false},
		{10, "new address", dns, []networkRoute{address}, false},
		{12, "address settling", dns, []networkRoute{address}, false},
		{13, "address settled", dns, []networkRoute{address}, true},
		{14, "new DNS set", changedDNS, []networkRoute{address}, false},
		{17, "DNS settled", changedDNS, []networkRoute{address}, true},
		{18, "new gateway", changedDNS, []networkRoute{gateway}, false},
		{19, "another address", changedDNS, []networkRoute{changedAgain}, false},
		{21, "still settling", changedDNS, []networkRoute{changedAgain}, false},
		{22, "route settled", changedDNS, []networkRoute{changedAgain}, true},
		{23, "disconnected", nil, nil, false},
		{26, "disconnect settled", nil, nil, true},
		{27, "reconnected", dns, []networkRoute{base}, false},
		{30, "reconnect settled", dns, []networkRoute{base}, true},
	}
	start := time.Unix(1000, 0)
	n := networkChange{current: networkStateFor(dns, []networkRoute{base}).key()}
	for _, event := range events {
		if got := n.observe(networkStateFor(event.dns, event.routes).key(), start.Add(time.Duration(event.second)*time.Second)); got != event.restart {
			t.Fatalf("%s at %ds: restart %v, want %v", event.name, event.second, got, event.restart)
		}
	}
}

func TestNetworkRouteSelection(t *testing.T) {
	a := networkRoute{name: "Ethernet", index: 1, gateway: "gateway-a", address: "address-a", metric: 10}
	b := networkRoute{name: "Wi-Fi", index: 2, gateway: "gateway-b", address: "address-b", metric: 20}
	v6 := a
	v6.ipv6 = true
	initial := networkStateFor(nil, []networkRoute{a, b, v6}).key()
	if got := networkStateFor(nil, []networkRoute{v6, b, a}).key(); got != initial {
		t.Fatal("route order changed network")
	}
	b.metric = 5
	if got := networkStateFor(nil, []networkRoute{a, b, v6}).key(); got == initial {
		t.Fatal("primary interface change was ignored")
	}
	for _, name := range []string{"utun3", "tun0", "tap0", "docker0", "wg0", "Loopback"} {
		virtual := a
		virtual.name, virtual.index, virtual.metric = name, 3, 0
		if got := networkStateFor(nil, []networkRoute{a, virtual, v6}).key(); got != initial {
			t.Fatalf("%s changed network", name)
		}
	}
}

func TestReadNetwork(t *testing.T) {
	// Exercise the native route reader without depending on network connectivity.
	if _, ok := readNetwork(); !ok {
		t.Fatal("cannot read native route table")
	}
}

func TestNetworkReaderFallbackRecordedRoutes(t *testing.T) {
	physical := networkRoute{name: "Ethernet", index: 1, metric: 10}
	for _, tc := range []struct {
		name   string
		routes []networkRoute
		err    error
	}{
		{"PPPoE", nil, nil},
		{"Hyper-V external switch", []networkRoute{{name: "vEthernet (External)", index: 2}}, nil},
		{"no adapter DNS", []networkRoute{physical}, nil},
		{"route table unavailable", nil, errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dns := []string{"a.example.org:53"}
			read := func() (networkState, bool) {
				return readNetworkWith(func() ([]networkRoute, error) { return tc.routes, tc.err },
					func([]networkRoute) []string { return nil }, func() []string { return dns })
			}
			state, ok := read()
			if ok != (tc.err == nil) || !slices.Equal(state.dns, dns) {
				t.Fatalf("state %+v, ok %v", state, ok)
			}
			n := networkChange{current: state.key()}
			now := time.Unix(1000, 0)
			dns = []string{"b.example.org:53"}
			state, ok = read()
			if n.observeState(state, ok, now) || !n.observeState(state, ok, now.Add(networkSettle)) {
				t.Fatal("resolver change did not restart after settling")
			}
		})
	}
}

func TestFailedNetworkReadKeepsPathAndComparesDNS(t *testing.T) {
	dns := []string{"a.example.org:53"}
	n := networkChange{current: networkStateFor(dns, []networkRoute{{name: "Ethernet", index: 1}}).key()}
	now := time.Unix(1000, 0)
	state := networkState{dns: dns}
	if n.observeState(state, false, now) || n.observeState(state, false, now.Add(networkSettle)) {
		t.Fatal("route failure alone restarted router")
	}
	state.dns = []string{"b.example.org:53"}
	if n.observeState(state, false, now) || !n.observeState(state, false, now.Add(networkSettle)) {
		t.Fatal("route failure hid DNS change")
	}
}

func TestNetworkIncludesVPNResolvers(t *testing.T) {
	routes := []networkRoute{{name: "Ethernet", index: 1}, {name: "tun0", index: 2}}
	dns := []string{"a.example.org:53"}
	read := func() networkState {
		state, ok := readNetworkWith(func() ([]networkRoute, error) { return routes, nil },
			func([]networkRoute) []string { return dns }, func() []string { t.Fatal("unexpected fallback"); return nil })
		if !ok {
			t.Fatal("route read failed")
		}
		return state
	}
	before := read()
	dns = append(dns, "vpn.example.org:53")
	after := read()
	if before.paths != after.paths || before.key() == after.key() || !slices.Equal(after.dns, dns) {
		t.Fatal("VPN DNS did not change network independently of physical routes")
	}
}

func TestNetworkIPv6PrivacyRecordedAddresses(t *testing.T) {
	iface := net.Interface{Index: 1, Name: "Ethernet", Flags: net.FlagUp}
	read := func(addresses ...string) string {
		var addrs []net.Addr
		for _, address := range addresses {
			ip, subnet, err := net.ParseCIDR(address)
			if err != nil {
				t.Fatal(err)
			}
			subnet.IP = ip
			addrs = append(addrs, subnet)
		}
		route, ok := interfaceRouteFor(iface, addrs, "gateway", 10, true)
		if !ok {
			t.Fatal("physical interface rejected")
		}
		return networkStateFor(nil, []networkRoute{route}).key()
	}
	initial := read("2001:db8:1::abcd/64", "2001:db8:1::ffff/64")
	if read("2001:db8:1::1/64", "2001:db8:1::abcd/64") != initial || read("2001:db8:1::2/64") != initial {
		t.Fatal("privacy address rotation changed key")
	}
	if read("2001:db8:2::1/64") == initial {
		t.Fatal("new prefix ignored")
	}
}

func TestNetworkEqualMetricsRecordedRoutes(t *testing.T) {
	a := networkRoute{name: "Ethernet", index: 2, metric: 10, address: "address-a"}
	b := networkRoute{name: "Wi-Fi", index: 10, metric: 10, address: "address-b"}
	for _, routes := range [][]networkRoute{{a, b}, {b, a}, {a, b, a}} {
		chosen := primaryRoutes(routes)
		if len(chosen) != 1 || chosen[0].index != a.index {
			t.Fatalf("unstable primary: %+v", chosen)
		}
	}
}

// A moment with no network at all (adapters re-binding, a VPN reconnecting)
// does not restart the router; only a usable state that differs from the
// running one does, after the settle time.
func TestNoNetworkStateDoesNotRestart(t *testing.T) {
	route := networkRoute{name: "Ethernet", index: 1, gateway: "gateway-a", address: "address-a", metric: 10}
	running := networkStateFor([]string{"a.example.org:53"}, []networkRoute{route})
	start := time.Unix(1000, 0)
	change := networkChange{current: running.key()}
	for i := range 10 {
		if change.observeState(networkState{}, true, start.Add(time.Duration(i)*2*time.Second)) {
			t.Fatalf("restart on a state without a network at second %d", i*2)
		}
	}
	// The same network comes back: nothing to restart.
	for i := range 4 {
		if change.observeState(running, true, start.Add(time.Duration(20+i*2)*time.Second)) {
			t.Fatal("restart although the same network returned")
		}
	}
	// A different network comes back after the gap: restarts once, after settling.
	other := networkStateFor([]string{"b.example.org:53"}, []networkRoute{route})
	restarts := 0
	for i := range 6 {
		if change.observeState(networkState{}, true, start.Add(time.Duration(40+i*4)*time.Second)) ||
			change.observeState(other, true, start.Add(time.Duration(42+i*4)*time.Second)) {
			restarts++
		}
	}
	if restarts != 1 {
		t.Fatalf("%d restarts after a different network returned, want 1", restarts)
	}
}
