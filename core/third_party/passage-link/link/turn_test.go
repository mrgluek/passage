package link

import (
	"strings"
	"testing"
)

func TestTURNRoute(t *testing.T) {
	for _, relay := range []string{"turn:turn.example.com", "turn://turn.example.com:3478?transport=udp", "turn:192.0.2.1:3478?transport=tcp", "turns:turn.example.com", "turns://turn.example.com:443/?transport=tcp", "turn:[2001:db8::1]:3478"} {
		r := Route{ID: "turn", Type: "turn", Wire: "turn-v1", Params: map[string]any{"relay": relay, "exit": "198.51.100.1:34780", "key": make([]byte, 32), "secret": "test-secret", "dial_ip": "192.0.2.1", "sni": "turn.example.com"}}
		if ok, err := ValidateRoute(r); err != nil || !ok {
			t.Fatalf("%s: %v", relay, err)
		}
		raw, err := Encode(bundle(r), nil)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(raw, nil)
		if err != nil || len(decoded.Unsupported) != 0 {
			t.Fatalf("decode: %v", err)
		}
		p, err := Params[TURNParams](decoded.Bundle.Routes[0])
		if err != nil || p.Relay != relay || p.Exit != "198.51.100.1:34780" {
			t.Fatalf("round trip: %+v %v", p, err)
		}
	}
}
func TestTURNRequiresCredentials(t *testing.T) {
	r := Route{ID: "turn", Type: "turn", Wire: "turn-v1", Params: map[string]any{"relay": "turn:turn.example.com", "exit": "192.0.2.1:34780", "key": make([]byte, 32)}}
	if _, err := ValidateRoute(r); err == nil {
		t.Fatal("route without a secret or credentials accepted")
	}
	r.Params["secret"] = "test-secret"
	if _, err := ValidateRoute(r); err != nil {
		t.Fatal(err)
	}
}
func TestTURNRejectsInvalidParameters(t *testing.T) {
	bad := map[string][]any{
		"relay": {"https://turn.example.com", "turn:", "turn://user:pass@turn.example.com", "turn://turn.example.com/#", "turn://turn.example.com/path", "turn://turn.example.com?", "turn://turn.example.com?transport=udp&x=y", "turns://turn.example.com?transport=udp", "turn://turn.example.com:0", "turn://turn.example.com:65536", "turn://turn.example.com:", "turn://bad_host", "turn://turn.example.com\n", "turn://turn.example.com\u007f", "turn://" + strings.Repeat("a", 1024)},
		"exit":  {"example.org:443", "192.0.2.1", "192.0.2.1:0"},
		"key":   {make([]byte, 31)}, "dial_ip": {"example.org", "192.0.2.1:443"}, "sni": {"192.0.2.1", "Example.org", "bad_host.example.org", strings.Repeat("a.", 127) + "org"},
		"secret": {strings.Repeat("s", 256)}, "username": {"only", strings.Repeat("u", 256)}, "credential": {"only", strings.Repeat("c", 256)},
	}
	for field, values := range bad {
		for _, v := range values {
			r := Route{ID: "turn", Type: "turn", Wire: "turn-v1", Params: map[string]any{"relay": "turn:turn.example.com", "exit": "192.0.2.1:34780", "key": make([]byte, 32)}}
			if field != "secret" && field != "username" && field != "credential" {
				r.Params["secret"] = "test-secret"
			}
			r.Params[field] = v
			if _, err := ValidateRoute(r); err == nil {
				t.Errorf("accepted %s=%v", field, v)
			}
		}
	}
	r := Route{ID: "turn", Type: "turn", Wire: "turn-v1", Params: map[string]any{"relay": "turn:turn.example.com", "exit": "192.0.2.1:34780", "key": make([]byte, 32), "username": "user", "credential": "password"}}
	if _, err := ValidateRoute(r); err != nil {
		t.Fatal(err)
	}
	r.Params["secret"] = "secret"
	if _, err := ValidateRoute(r); err == nil {
		t.Fatal("combined credentials accepted")
	}
}
