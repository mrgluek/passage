package main

import (
	"strings"
	"testing"
)

func TestLaunchLink(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
		bad  bool
	}{
		{name: "none"},
		{name: "routes", args: []string{"passage://routes?data=example"}, want: "passage://routes?data=example"},
		{name: "subscription", args: []string{"passage://subscribe?data=example"}, want: "passage://subscribe?data=example"},
		{name: "scheme case", args: []string{"PASSAGE://routes"}, want: "PASSAGE://routes"},
		{name: "wrong scheme", args: []string{"https://example.org/link"}, bad: true},
		{name: "empty", args: []string{""}, bad: true},
		{name: "malformed", args: []string{"passage://%"}, bad: true},
		{name: "opaque", args: []string{"passage:example"}, bad: true},
		{name: "multiple", args: []string{"passage://routes", "passage://routes"}, bad: true},
		{name: "long", args: []string{"passage://" + strings.Repeat("a", maxLaunchLink)}, bad: true},
		{name: "limit", args: []string{"passage://" + strings.Repeat("a", maxLaunchLink-len("passage://"))}, want: "passage://" + strings.Repeat("a", maxLaunchLink-len("passage://"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := launchLink(tc.args)
			if got != tc.want || (err != nil) != tc.bad {
				t.Fatalf("link %q, err %v", got, err)
			}
			if tc.bad && launchImport(tc.args) != "passage://invalid" {
				t.Fatal("invalid launch must reach import error")
			}
		})
	}
}
