package link

import "testing"

func TestWSSRouteRoundTripAndValidation(t *testing.T) {
	r := Route{ID: "web", Type: "wss", Wire: "wss-v1", Params: map[string]any{"endpoint": "wss://example.org/passage", "key": make([]byte, 32)}}
	raw, err := Encode(bundle(r), nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Decode(raw, nil)
	if err != nil || len(d.Bundle.Routes) != 1 {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"ws://example.org/passage", "wss://user:pass@example.org/", "wss://example.org/?token=x", "wss://example.org/?", "wss://example.org/#", "wss://example.org:0/", "wss://example.org:65536/", "wss://example.org:/", "wss://bad_host/", "wss://example.org/ x"} {
		r.Params["endpoint"] = endpoint
		if _, err := ValidateRoute(r); err == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
	r.Params["endpoint"] = "wss://example.org/passage"
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
