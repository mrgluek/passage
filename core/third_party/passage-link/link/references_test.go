package link

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func referencesBundle() Bundle {
	return Bundle{V: 2, Kind: "routes", Mode: "replace", Sub: bytes.Repeat([]byte{1}, 16), Revision: 1, Routes: []Route{{ID: "web", Type: "wss", Wire: "wss-v1", Params: map[string]any{"endpoint": "wss://example.org/route", "key": bytes.Repeat([]byte{7}, 32)}}}}
}

func TestBundleReferences(t *testing.T) {
	b := referencesBundle()
	b.References = []string{"example.org:443", "example.net:8443"}
	encoded, err := Encode(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Bundle.References) != 2 || decoded.Bundle.References[1] != "example.net:8443" {
		t.Fatalf("references lost: %v", decoded.Bundle.References)
	}
	again, err := Encode(decoded.Bundle, nil)
	if err != nil || !bytes.Equal(encoded, again) {
		t.Fatal("round trip changed", err)
	}
	// A bundle without the key decodes with no references.
	old, err := Encode(referencesBundle(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := Decode(old, nil); err != nil || len(d.Bundle.References) != 0 {
		t.Fatalf("old bundle: %v %v", d.Bundle.References, err)
	}
}

func TestBundleReferencesInvalid(t *testing.T) {
	for name, change := range map[string]func(*Bundle){
		"count":  func(b *Bundle) { b.References = repeat08("example.org:443", 9) },
		"port":   func(b *Bundle) { b.References = []string{"example.org:0"} },
		"host":   func(b *Bundle) { b.References = []string{":443"} },
		"space":  func(b *Bundle) { b.References = []string{"a b:443"} },
		"closed": func(b *Bundle) { b.Closed = true; b.Routes = nil; b.References = []string{"example.org:443"} },
	} {
		b := referencesBundle()
		change(&b)
		if _, err := Encode(b, nil); err == nil {
			t.Errorf("%s: invalid references accepted", name)
		}
	}
	if err := ValidateReferences(repeat08("example.org:443", 8)); err != nil {
		t.Fatal(err)
	}
}

// An explicit empty list stays distinct from an absent one through JSON and CBOR.
func TestBundleReferencesExplicitEmpty(t *testing.T) {
	b := referencesBundle()
	b.References = []string{}
	encoded, err := Encode(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	absent, err := Encode(referencesBundle(), nil)
	if err != nil || bytes.Equal(encoded, absent) {
		t.Fatal("empty list encodes like an absent one", err)
	}
	decoded, err := Decode(encoded, nil)
	if err != nil || decoded.Bundle.References == nil || len(decoded.Bundle.References) != 0 {
		t.Fatalf("empty list lost: %#v %v", decoded.Bundle.References, err)
	}
	again, err := Encode(decoded.Bundle, nil)
	if err != nil || !bytes.Equal(encoded, again) {
		t.Fatal("round trip changed", err)
	}
	if d, err := Decode(absent, nil); err != nil || d.Bundle.References != nil {
		t.Fatalf("absent key decoded as %#v %v", d.Bundle.References, err)
	}
	js, err := json.Marshal(b)
	if err != nil || !strings.Contains(string(js), `"references":[]`) {
		t.Fatalf("json: %s %v", js, err)
	}
	var back Bundle
	if err = json.Unmarshal(js, &back); err != nil || back.References == nil {
		t.Fatalf("json round trip: %#v %v", back.References, err)
	}
	if js, _ = json.Marshal(referencesBundle()); strings.Contains(string(js), "references") {
		t.Fatalf("absent list marshaled: %s", js)
	}
}
