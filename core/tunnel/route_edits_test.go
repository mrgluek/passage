package tunnel

import (
	"testing"

	links "github.com/mrgluek/passage-link/link"
)

func TestEdited(t *testing.T) {
	routes := []links.Route{{ID: "a", Priority: 10}, {ID: "b", Priority: 20}, {ID: "c", Priority: 30}}
	p := uint16(5)
	got := Edited(routes, map[string]RouteEdit{"c": {Priority: &p}, "a": {Off: true}, "zz": {Off: true}})
	if len(got) != 2 || got[0].ID != "c" || got[0].Priority != 5 || got[1].ID != "b" {
		t.Fatalf("got %+v", got)
	}
	if routes[2].Priority != 30 {
		t.Fatal("input changed")
	}
	if got := Edited(routes, nil); len(got) != 3 || got[0].ID != "a" {
		t.Fatalf("no edits: %+v", got)
	}
}

func TestMinimalEdits(t *testing.T) {
	routes := []links.Route{{ID: "a", Priority: 10}, {ID: "b", Priority: 20}}
	same, other := uint16(10), uint16(7)
	got := MinimalEdits(routes, map[string]RouteEdit{"a": {Priority: &same}, "b": {Priority: &other, Off: true}, "gone": {Off: true}})
	if len(got) != 1 || got["b"].Priority == nil || *got["b"].Priority != 7 || !got["b"].Off {
		t.Fatalf("got %+v", got)
	}
	if _, ok := got["a"]; ok {
		t.Fatal("unchanged priority kept")
	}
}

func TestImportKeepsEditsOnMergeAndClearsOnReplace(t *testing.T) {
	route := func(id string) links.Route {
		return links.Route{ID: id, Type: "soar", Wire: "soar-v1", Priority: 100, Params: map[string]any{"zone": "tunnel.example.org", "key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}
	}
	link := func(mode, id string) string {
		raw, err := links.Encode(links.Bundle{V: 2, Kind: "routes", Mode: mode, Routes: []links.Route{route(id)}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		uri, _ := links.URI(raw, "")
		return uri
	}
	p := uint16(5)
	existing := Config{Routes: []links.Route{route("a")}, RouteEdits: map[string]RouteEdit{"a": {Priority: &p}}}
	merged, err := ImportLink(existing, link("merge", "b"), false)
	if err != nil || merged.Config.RouteEdits["a"].Priority == nil {
		t.Fatalf("merge lost the edit: %v %+v", err, merged.Config.RouteEdits)
	}
	replaced, err := ImportLink(existing, link("replace", "b"), false)
	if err != nil || len(replaced.Config.RouteEdits) != 0 {
		t.Fatalf("replace kept edits: %v %+v", err, replaced.Config.RouteEdits)
	}
}
