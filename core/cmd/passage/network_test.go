package main

import (
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
