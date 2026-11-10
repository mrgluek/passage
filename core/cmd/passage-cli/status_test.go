package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mrgluek/passage/core/tunnel"
)

func TestStatusConnectionEvidence(t *testing.T) {
	var status tunnel.Status
	err := json.Unmarshal([]byte(`{"groups":{"relay.example.org":{"route":"direct","state":"direct","targets":[],"last_conn":"2026-01-01T00:00:00Z","last_conn_result":{"outcome":"ok","route":"direct"}}}}`), &status)
	if err != nil {
		t.Fatal(err)
	}
	line := string(statusJSON(status))
	if !strings.Contains(line, `"last_conn":"2026-01-01T00:00:00Z"`) || !strings.Contains(line, `"last_conn_result":{"outcome":"ok","route":"direct"}`) {
		t.Fatal(line)
	}
}

func TestStatusWithoutConnectionEvidence(t *testing.T) {
	status := tunnel.Status{Groups: map[string]tunnel.GroupStatus{"relay.example.org": {Route: "direct", State: "unknown", Targets: []string{}}}}
	line := string(statusJSON(status))
	want := `{"active":null,"groups":{"relay.example.org":{"route":"direct","state":"unknown","targets":[]}},"route":"","state":"","stats":null}`
	if line != want {
		t.Fatalf("got %s, want %s", line, want)
	}
}
