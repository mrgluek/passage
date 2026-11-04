package main

import (
	"net/netip"
	"testing"
)

func TestPolicyRejectsUnconfiguredDestinations(t *testing.T) {
	dial, err := policy([]rule{{Host: "mail.example.org", Ports: []uint16{443, 993}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"other.example.org", "127.0.0.1", "mail.example.org.evil"} {
		if c, status := dial(host, 443); c != nil || status != 1 {
			t.Fatalf("%s allowed", host)
		}
	}
	if c, status := dial("mail.example.org", 25); c != nil || status != 1 {
		t.Fatal("SMTP port allowed")
	}
}
func TestPolicyValidationAndPrivateAddresses(t *testing.T) {
	for _, rules := range [][]rule{nil, {{CIDR: "0.0.0.0/0", Ports: []uint16{443}}}, {{Host: "mail.example.org", Ports: []uint16{25}}}, {{Host: "mail.example.org", CIDR: "192.0.2.0/24", Ports: []uint16{443}}}} {
		if _, err := policy(rules); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	for _, s := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::ffff:192.168.1.1"} {
		if public(netip.MustParseAddr(s)) {
			t.Fatalf("%s is public", s)
		}
	}
}
