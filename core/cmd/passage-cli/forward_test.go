package main

import (
	"encoding/json"
	"testing"

	"github.com/mrgluek/passage/core/tunnel"
)

func TestParseForward(t *testing.T) {
	for _, value := range []string{"", "127.0.0.1:18001", "=example.org:443", "127.0.0.1:18001="} {
		if _, err := parseForward(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	f, err := parseForward("127.0.0.1:18001=example.org:443")
	if err != nil || f.Listen != "127.0.0.1:18001" || f.Target != "example.org:443" {
		t.Fatalf("forward = %+v, %v", f, err)
	}
}

func TestForwardStatusJSON(t *testing.T) {
	row := tunnel.ForwardStatus{Name: "test", Listen: "127.0.0.1:18001", Target: "example.org:443", Up: true, Conns: 2}
	var result struct {
		Forwards []tunnel.ForwardStatus `json:"forwards"`
	}
	if err := json.Unmarshal(statusJSON(tunnel.Status{Forwards: []tunnel.ForwardStatus{row}}), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Forwards) != 1 || result.Forwards[0] != row {
		t.Fatalf("status forwards = %+v", result.Forwards)
	}
}
