package link

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func fixture(id string) Route {
	return Route{ID: id, Type: "soar", Wire: "soar-v1", Priority: 100, Params: map[string]any{"zone": "tunnel.example.org", "key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}}
}
func bundle(routes ...Route) Bundle {
	return Bundle{V: 2, Kind: "routes", Mode: "merge", IssuedAt: 1793491200, Routes: routes}
}
func TestSignedTrustAndTampering(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	raw, err := Encode(bundle(fixture("dns")), key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw, []ed25519.PublicKey{pub})
	if err != nil || decoded.Signature != "trusted_author" {
		t.Fatalf("decode %s: %v", decoded.Signature, err)
	}
	unknown, err := Decode(raw, nil)
	if err != nil || unknown.Signature != "unknown_signer" {
		t.Fatal("unknown signer must not be trusted", err)
	}
	again, _ := Encode(bundle(fixture("dns")), key)
	if !bytes.Equal(raw, again) {
		t.Fatal("non-deterministic signature")
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-1] ^= 1
	if _, err = Decode(tampered, nil); err == nil {
		t.Fatal("tampered signature accepted")
	}
	var tagged cbor.RawTag
	dec.Unmarshal(raw, &tagged)
	var parts []cbor.RawMessage
	dec.Unmarshal(tagged.Content, &parts)
	var body []byte
	dec.Unmarshal(parts[2], &body)
	if _, err = Decode(body, nil); err == nil {
		t.Fatal("stripped signature accepted")
	}
	uri, err := URI(raw, "https://example.org/link")
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseURI(uri, []ed25519.PublicKey{pub})
	if err != nil || d.Signature != "trusted_author" {
		t.Fatal(err)
	}
	if _, err = ParseURI(strings.Replace(uri, "#", "?credential=x#", 1), nil); err == nil {
		t.Fatal("query accepted")
	}
}
func TestMalformedAndUnsupported(t *testing.T) {
	tests := map[string][]byte{"duplicate": {0xa2, 0x61, 'v', 2, 0x61, 'v', 2}, "indefinite": {0xbf, 0xff}, "trailing": {0xa0, 0xa0}, "nonminimal": {0xa1, 0x61, 'v', 0x18, 2}}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(raw, nil); err == nil {
				t.Fatal("accepted malformed document")
			}
		})
	}
	b := bundle(fixture("dns"))
	raw, _ := enc.Marshal(b)
	var m map[string]any
	dec.Unmarshal(raw, &m)
	m["optional"] = float64(1.5)
	raw, _ = enc.Marshal(m)
	if _, err := Decode(raw, nil); err == nil {
		t.Fatal("float accepted")
	}
	m["optional"] = "future"
	raw, _ = enc.Marshal(m)
	if _, err := Decode(raw, nil); err != nil {
		t.Fatal("optional field rejected", err)
	}
	b.Required = []string{"future-feature"}
	if _, err := Encode(b, nil); err == nil {
		t.Fatal("required feature ignored")
	}
	b = bundle(fixture("dns"), Route{ID: "new", Type: "future", Wire: "future/1", Params: map[string]any{}})
	raw, err := Encode(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Decode(raw, nil)
	if err != nil || len(d.Unsupported) != 1 {
		t.Fatal("unsupported route handling", err)
	}
	b.Routes[0].Params["key"] = "broken"
	if _, err := Encode(b, nil); err == nil {
		t.Fatal("malformed supported route skipped")
	}
}
func TestAtomicAddAndReplace(t *testing.T) {
	existing := []Route{fixture("dns")}
	newRoute := fixture("backup")
	added, err := Apply(existing, bundle(newRoute), false)
	if err != nil || len(added) != 2 || len(existing) != 1 {
		t.Fatal("merge failed", err)
	}
	changed := fixture("dns")
	changed.Priority = 10
	if _, err := Apply(existing, bundle(changed), false); err == nil || existing[0].Priority != 100 {
		t.Fatal("collision silently replaced")
	}
	replaced, err := Apply(existing, bundle(changed), true)
	if err != nil || replaced[0].Priority != 10 {
		t.Fatal(err)
	}
	clear := bundle(newRoute)
	clear.Mode = "replace"
	replaced, err = Apply(existing, clear, false)
	if err != nil || len(replaced) != 1 || replaced[0].ID != "backup" {
		t.Fatal("replace failed", err)
	}
	clear.Routes[0].Params["key"] = "bad"
	if _, err = Apply(existing, clear, true); err == nil || existing[0].ID != "dns" {
		t.Fatal("failed replace changed installed routes")
	}
}
func TestLegacyAndTicket(t *testing.T) {
	text := "https://example.org/link#v=1&zone=tunnel.example.org&key=" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)) + "&r=192.0.2.1:53&ipv6=1"
	r, err := ImportV1(text, "dns")
	if err != nil || !r.IPv6 {
		t.Fatal(err)
	}
	if _, err = ImportV1(text+"&key=other", "dns"); err == nil {
		t.Fatal("duplicate key accepted")
	}
	relay := "https://relay.example.org./"
	raw := append([]byte{0}, bytes.Repeat([]byte{8}, 32)...)
	raw = append(raw, 1)
	raw = binary.AppendUvarint(raw, uint64(len(relay)))
	raw = append(raw, relay...)
	raw = append(raw, 0)
	ticket := "node" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	if err := validateTicket(ticket, "https://relay.example.org:443"); err != nil {
		t.Fatal(err)
	}
	if err := validateTicket(ticket, "https://other.example.org"); err == nil {
		t.Fatal("mismatched relay accepted")
	}
	raw[len(raw)-1] = 1
	ticket = "node" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	if err := validateTicket(ticket, relay); err == nil {
		t.Fatal("direct hint accepted")
	}
}
func FuzzDecode(f *testing.F) {
	raw, _ := Encode(bundle(fixture("dns")), nil)
	f.Add(raw)
	f.Add([]byte{0xd2})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxBytes {
			return
		}
		Decode(data, nil)
	})
}

func TestValidateUpdateAddresses(t *testing.T) {
	route := Route{ID: "dns", Type: "soar", Wire: "soar-v1", Priority: 1, Params: map[string]any{"zone": "t.example.org", "key": make([]byte, 32)}}
	base := Bundle{V: 2, Kind: "routes", Mode: "merge", Routes: []Route{route}}
	for _, updates := range [][]string{{"http://example.org/repo"}, {"https://"}, {"https://a.example", "https://b.example", "https://c.example", "https://d.example", "https://e.example"}} {
		b := base
		b.Updates = updates
		if _, err := b.Validate(); err == nil {
			t.Errorf("accepted %v", updates)
		}
	}
}
