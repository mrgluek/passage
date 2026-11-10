package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mrgluek/passage/core/tunnel"
)

func TestStatusSubscriptionSources(t *testing.T) {
	var status tunnel.Status
	if err := json.Unmarshal([]byte(`{"subscriptions":[{"id":"example","sources":[{"id":"bundle-1","tries":2,"answered":1,"ok":1,"via":{"tunnel":1}}]}]}`), &status); err != nil {
		t.Fatal(err)
	}
	line := string(statusJSON(status))
	if !strings.Contains(line, `"sources":[{"id":"bundle-1","tries":2,"answered":1,"ok":1,"via":{"tunnel":1}}]`) {
		t.Fatal(line)
	}
}
