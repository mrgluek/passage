package link

import (
	"strings"
	"testing"
)

func TestHTTPRouteRoundTripAndValidation(t *testing.T) {
	r := Route{ID: "cdn", Type: "http", Wire: "http-v1", Params: map[string]any{"endpoint": "https://example.org/passage", "key": make([]byte, 32), "dial_ip": "192.0.2.1", "sni": "example.com"}}
	raw, err := Encode(bundle(r), nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw, nil)
	if err != nil || len(decoded.Unsupported) != 0 {
		t.Fatalf("HTTP not supported: %v", err)
	}
	params, err := Params[HTTPParams](decoded.Bundle.Routes[0])
	if err != nil || params.DialIP != "192.0.2.1" || params.SNI != "example.com" {
		t.Fatal("parameters lost", err)
	}
	for _, endpoint := range []string{"http://example.org/", "https://user:pass@example.org/", "https://example.org/?x=y", "https://example.org/?", "https://example.org/#", "https://example.org:0/", "https://example.org:/", "https://bad_host/", "https://example.org/ x"} {
		r.Params["endpoint"] = endpoint
		if _, err := ValidateRoute(r); err == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
	r.Params["endpoint"] = "https://example.org/passage"
	for _, ip := range []string{"edge.example.org", "192.0.2.1:443", "[::1]", "not an IP"} {
		r.Params["dial_ip"] = ip
		if _, err := ValidateRoute(r); err == nil {
			t.Errorf("accepted dial_ip %q", ip)
		}
	}
	delete(r.Params, "dial_ip")
	for _, sni := range []string{"", "example.org", "edge.example.com", strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)} {
		r.Params["sni"] = sni
		if _, err := ValidateRoute(r); err != nil {
			t.Errorf("rejected sni %q: %v", sni, err)
		}
	}
	for _, sni := range []string{"192.0.2.1", "Example.org", "example.org:443", "example.org ", "bad_host.example.org", "example.org.", "-edge.example.org", "example..org", strings.Repeat("a", 64) + ".example.org", strings.Repeat("a.", 127) + "org"} {
		r.Params["sni"] = sni
		if _, err := ValidateRoute(r); err == nil {
			t.Errorf("accepted sni %q", sni)
		}
	}
	delete(r.Params, "sni")
	r.Params["key"] = make([]byte, 16)
	if _, err := ValidateRoute(r); err == nil {
		t.Fatal("short key accepted")
	}
	r.Params["key"] = make([]byte, 32)
	r.Params["unknown"] = true
	if _, err := ValidateRoute(r); err == nil {
		t.Fatal("unknown parameter accepted")
	}
}
