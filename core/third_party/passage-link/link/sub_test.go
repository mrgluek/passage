package link

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

func subFixture() (Bundle, []byte, []byte, ed25519.PrivateKey) {
	id := bytes.Repeat([]byte{1}, 16)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, 32))
	read, _ := DeriveReadKey(key, id)
	return Bundle{V: 2, Kind: "routes", Mode: "replace", Name: "Example", Sub: id, Revision: 1, Routes: []Route{fixture("dns")}}, id, read, key
}
func inviteFixture() (Invite, ed25519.PrivateKey) {
	b, id, read, key := subFixture()
	_ = b
	return Invite{V: 2, Kind: "sub", ID: id, Name: "Example", Sources: []string{"https://example.org/sub/file.bin"}, ReadKey: read, Required: []string{"sub"}}, key
}
func TestSubRoundTripAndBoundaries(t *testing.T) {
	b, id, read, key := subFixture()
	pub := key.Public().(ed25519.PublicKey)
	blob, err := SealSubFile(b, id, read, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 1024+SubFileOverhead {
		t.Fatal("small bucket", len(blob))
	}
	d, err := OpenSubFile(blob, id, read, pub)
	if err != nil || d.Bundle.Revision != 1 || d.Signature != "trusted_author" {
		t.Fatal(d, err)
	}
	other := bytes.Repeat([]byte{9}, 16)
	wrongKey := bytes.Repeat([]byte{9}, 32)
	wrongPub := ed25519.NewKeyFromSeed(wrongKey).Public().(ed25519.PublicKey)
	for name, run := range map[string]func() error{
		"key":    func() error { _, e := OpenSubFile(blob, id, wrongKey, pub); return e },
		"id":     func() error { _, e := OpenSubFile(blob, other, read, pub); return e },
		"signer": func() error { _, e := OpenSubFile(blob, id, read, wrongPub); return e },
		"oversize": func() error {
			_, e := OpenSubFile(make([]byte, MaxSubFileBytes+SubFileOverhead+1), id, read, pub)
			return e
		},
		"unsigned": func() error { _, e := SealSubFile(b, id, read, nil); return e },
	} {
		t.Run(name, func(t *testing.T) {
			if run() == nil {
				t.Fatal("accepted invalid file")
			}
		})
	}
	for i := range blob {
		changed := bytes.Clone(blob)
		changed[i] ^= 1
		if _, e := OpenSubFile(changed, id, read, pub); e == nil {
			t.Fatalf("tampered byte %d accepted", i)
		}
	}
	for i := 0; i < len(blob); i++ {
		if _, e := OpenSubFile(blob[:i], id, read, pub); e == nil {
			t.Fatalf("truncation %d accepted", i)
		}
	}
	// Re-encrypt a valid signed bundle from another subscription using this file's AAD.
	foreign := b
	foreign.Sub = other
	raw, _ := Encode(foreign, key)
	plain := make([]byte, 1024)
	plain[2] = byte(len(raw) >> 8)
	plain[3] = byte(len(raw))
	copy(plain[4:], raw)
	aead, _ := chacha20poly1305.NewX(read)
	swapped := aead.Seal(bytes.Clone(blob[:29]), blob[5:29], plain, append([]byte("PSUB1"), id...))
	if _, e := OpenSubFile(swapped, id, read, pub); e == nil {
		t.Fatal("foreign signed subscription accepted")
	}
	plain[len(plain)-1] = 1
	badPadding := aead.Seal(bytes.Clone(blob[:29]), blob[5:29], plain, append([]byte("PSUB1"), id...))
	if _, e := OpenSubFile(badPadding, id, read, pub); e == nil {
		t.Fatal("nonzero padding accepted")
	}
}
func TestSubBucketsAndRouteLimit(t *testing.T) {
	b, id, read, key := subFixture()
	for n := 2; n <= MaxSubRoutes; n++ {
		r := fixture(fmt.Sprintf("dns%d", n))
		b.Routes = append(b.Routes, r)
	}
	blob, err := SealSubFile(b, id, read, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 4096+SubFileOverhead {
		t.Fatal("large bucket", len(blob))
	}
	if _, err = OpenSubFile(blob, id, read, key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	plain := b
	plain.Sub = nil
	plain.Revision = 0
	if _, err = plain.Validate(); err == nil {
		t.Fatal("plain route limit extended")
	}
	b.Routes = append(b.Routes, fixture("extra"))
	if _, err = SealSubFile(b, id, read, key); err == nil {
		t.Fatal("too many routes")
	}
	b.Routes = b.Routes[:16]
	for i := range b.Routes {
		b.Routes[i].Params["canaries"] = []string{strings.Repeat("a", 240) + ".example.org:443", strings.Repeat("b", 240) + ".example.org:443", strings.Repeat("c", 240) + ".example.org:443", strings.Repeat("d", 240) + ".example.org:443", strings.Repeat("e", 240) + ".example.org:443"}
	}
	if _, err = SealSubFile(b, id, read, key); err == nil || !strings.Contains(err.Error(), "largest padding bucket") {
		t.Fatal("oversize bundle", err)
	}
	b, _, _, _ = subFixture()
	b.Revision = 0
	if _, err = b.Validate(); err == nil {
		t.Fatal("zero revision accepted")
	}
	b.Revision = 1
	b.Sub = []byte{1}
	if _, err = b.Validate(); err == nil {
		t.Fatal("short sub accepted")
	}
}
func TestSubRevision(t *testing.T) {
	if !errors.Is(CheckRevision(4, 3), ErrRollback) || !errors.Is(CheckRevision(4, 4), ErrNotNewer) || CheckRevision(4, 5) != nil || !errors.Is(CheckRevision(0, 0), ErrRollback) {
		t.Fatal("revision policy")
	}
}

type changingSigner struct {
	ed25519.PrivateKey
	count byte
}

func (s *changingSigner) Sign(_ io.Reader, _ []byte, _ crypto.SignerOpts) ([]byte, error) {
	s.count++
	return bytes.Repeat([]byte{s.count}, 64), nil
}
func TestDeriveReadKey(t *testing.T) {
	_, id, read, key := subFixture()
	again, err := DeriveReadKey(key, id)
	if err != nil || !bytes.Equal(read, again) {
		t.Fatal("unstable key", err)
	}
	other, err := DeriveReadKey(key, bytes.Repeat([]byte{2}, 16))
	if err != nil || bytes.Equal(read, other) {
		t.Fatal("id not bound", err)
	}
	if _, err = DeriveReadKey(&changingSigner{PrivateKey: key}, id); err == nil {
		t.Fatal("non-deterministic signer accepted")
	}
}
func TestInviteValidationAndWrappers(t *testing.T) {
	i, key := inviteFixture()
	raw, err := EncodeInvite(i, key)
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public().(ed25519.PublicKey)
	for _, base := range []string{"", "https://example.org/link"} {
		uri, err := URI(raw, base)
		if err != nil {
			t.Fatal(err)
		}
		d, err := DecodeInvite([]byte(uri), []ed25519.PublicKey{pub})
		if err != nil || d.Signature != "trusted_author" {
			t.Fatal(err)
		}
		if _, err = ParseURI(uri, nil); err == nil || !strings.Contains(err.Error(), "update Passage") {
			t.Fatal("old decoder path", err)
		}
	}
	d, err := DecodeInvite(raw, nil)
	if err != nil || d.Signature != "unknown_signer" {
		t.Fatal(err)
	}
	for _, source := range []string{"https://user@example.org/a", "https://example.org/a?x=1", "https://example.org/a?", "https://example.org/a#x", "https://example.org/a#", "http://example.org/a", "https:///a", "https://bad_host.example.org/a", "https://example.org:0/a", "https://[example.org]/a"} {
		t.Run(source, func(t *testing.T) {
			bad := i
			bad.Sources = []string{source}
			if _, err := EncodeInvite(bad, key); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
	for _, change := range []func(*Invite){func(i *Invite) { i.Sources = make([]string, 5) }, func(i *Invite) { i.ID = []byte{1} }, func(i *Invite) { i.ReadKey = []byte{1} }, func(i *Invite) { i.Required = nil }, func(i *Invite) { i.Name = strings.Repeat("x", 129) }, func(i *Invite) { i.Seed = []byte{1} }} {
		bad := i
		change(&bad)
		if _, err := EncodeInvite(bad, key); err == nil {
			t.Fatal("invalid invite accepted")
		}
	}
	raw[len(raw)-1] ^= 1
	if _, err := DecodeInvite(raw, nil); err == nil {
		t.Fatal("invite tampering accepted")
	}
	b, id, read, _ := subFixture()
	blob, _ := SealSubFile(b, id, read, key)
	i.Seed = blob
	raw, err = EncodeInvite(i, key)
	if err != nil {
		t.Fatal(err)
	}
	text, err := InviteURI(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeInvite([]byte(text), nil); err != nil {
		t.Fatal(err)
	}
	old, _ := Encode(bundle(fixture("dns")), key)
	if _, err = Decode(old, nil); err != nil {
		t.Fatal("plain regression", err)
	}
}
func TestSubGoldenVector(t *testing.T) {
	b, id, read, key := subFixture()
	nonce := bytes.Repeat([]byte{4}, 24)
	blob, err := sealSubFile(b, id, read, key, bytes.NewReader(nonce))
	if err != nil {
		t.Fatal(err)
	}
	i, _ := inviteFixture()
	i.Seed = blob
	invite, err := EncodeInvite(i, key)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := Encode(b, key)
	if err != nil {
		t.Fatal(err)
	}
	vector := map[string]string{"seed_hex": hex.EncodeToString(key.Seed()), "public_key_hex": hex.EncodeToString(key.Public().(ed25519.PublicKey)), "id_hex": hex.EncodeToString(id), "read_key_hex": hex.EncodeToString(read), "nonce_hex": hex.EncodeToString(nonce), "signed_bundle_hex": hex.EncodeToString(signed), "file_hex": hex.EncodeToString(blob), "invite_hex": hex.EncodeToString(invite)}
	encoded, _ := json.MarshalIndent(vector, "", "  ")
	encoded = append(encoded, '\n')
	path := "testdata/sub-v1.json"
	if os.Getenv("UPDATE_SUB_VECTOR") == "1" {
		if err = os.WriteFile(path, encoded, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(want, encoded) {
		t.Fatal("golden vector changed", err)
	}
	if _, err = OpenSubFile(blob, id, read, key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
}

func TestSubAllPaddingBuckets(t *testing.T) {
	for _, tc := range []struct{ extra, bucket int }{{0, 1024}, {1100, 2048}, {2400, 4096}, {4500, 8192}, {9000, 16384}} {
		b, id, read, key := subFixture()
		if tc.extra > 0 {
			b.Routes[0].Params["canaries"] = []string{strings.Repeat("a", tc.extra) + ".example.org:443"}
		}
		blob, err := SealSubFile(b, id, read, key)
		if err != nil {
			t.Fatal(err)
		}
		if len(blob) != tc.bucket+SubFileOverhead {
			t.Fatalf("extra %d: size %d", tc.extra, len(blob))
		}
		if _, err = OpenSubFile(blob, id, read, key.Public().(ed25519.PublicKey)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestInviteStrictCBOR(t *testing.T) {
	i, key := inviteFixture()
	i.Signer = key.Public().(ed25519.PublicKey)
	body, _ := enc.Marshal(i)
	var fields map[string]any
	if err := dec.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]any){func(m map[string]any) { delete(m, "name") }, func(m map[string]any) { m["name"] = nil }, func(m map[string]any) { m["extra"] = true }, func(m map[string]any) { m["seed"] = nil }} {
		copy := map[string]any{}
		for k, v := range fields {
			copy[k] = v
		}
		change(copy)
		body, _ := enc.Marshal(copy)
		raw, err := signBody(body, i.Signer, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeInvite(raw, nil); err == nil {
			t.Fatal("invalid shape accepted")
		}
	}
}

func TestLargeSeededInvite(t *testing.T) {
	b, id, read, key := subFixture()
	b.Routes[0].Params["canaries"] = []string{strings.Repeat("a", 9000) + ".example.org:443"}
	blob, err := SealSubFile(b, id, read, key)
	if err != nil {
		t.Fatal(err)
	}
	i, _ := inviteFixture()
	i.Seed = blob
	raw, err := EncodeInvite(i, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = URI(raw, ""); err == nil {
		t.Fatal("ordinary text limit extended")
	}
	text, err := InviteURI(raw, "")
	if err != nil || len(text) <= 16384 || len(text) > MaxBytes {
		t.Fatal("seed allowance", err)
	}
	if _, err = DecodeInvite([]byte(" "+text+"\n"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = InviteURI(make([]byte, MaxBytes), ""); err == nil {
		t.Fatal("hard text cap exceeded")
	}
	if _, err = DecodeInvite([]byte(strings.Repeat("x", MaxBytes+1)), nil); err == nil {
		t.Fatal("hard decode cap exceeded")
	}
}

func TestClosedSubGoldenVector(t *testing.T) {
	b, id, read, key := subFixture()
	b.Closed, b.Routes = true, []Route{}
	nonce := bytes.Repeat([]byte{4}, 24)
	blob, err := sealSubFile(b, id, read, key, bytes.NewReader(nonce))
	if err != nil {
		t.Fatal(err)
	}
	i, _ := inviteFixture()
	i.Seed = blob
	invite, err := EncodeInvite(i, key)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := Encode(b, key)
	if err != nil {
		t.Fatal(err)
	}
	vector := map[string]string{"seed_hex": hex.EncodeToString(key.Seed()), "public_key_hex": hex.EncodeToString(key.Public().(ed25519.PublicKey)), "id_hex": hex.EncodeToString(id), "read_key_hex": hex.EncodeToString(read), "nonce_hex": hex.EncodeToString(nonce), "signed_bundle_hex": hex.EncodeToString(signed), "file_hex": hex.EncodeToString(blob), "invite_hex": hex.EncodeToString(invite)}
	encoded, _ := json.MarshalIndent(vector, "", "  ")
	encoded = append(encoded, '\n')
	path := "testdata/sub-v1-closed.json"
	if os.Getenv("UPDATE_SUB_VECTOR") == "1" {
		if err = os.WriteFile(path, encoded, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(want, encoded) {
		t.Fatal("golden vector changed", err)
	}
	if _, err = OpenSubFile(blob, id, read, key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
}

func TestClosedSub(t *testing.T) {
	b, id, read, key := subFixture()
	blob, err := SealClosedSubFile(id, read, 2, key)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenSubFile(blob, id, read, key.Public().(ed25519.PublicKey))
	if err != nil || !opened.Bundle.Closed || opened.Bundle.Revision != 2 || len(opened.Bundle.Routes) != 0 || len(blob) != 1024+SubFileOverhead {
		t.Fatal(opened, err)
	}
	b.Closed = true
	if _, err := b.Validate(); err == nil {
		t.Fatal("closed routes accepted")
	}
	b.Routes = nil
	b.Sub = nil
	b.Revision = 0
	if _, err := b.Validate(); err == nil {
		t.Fatal("closed without sub accepted")
	}
	b.Closed = false
	if _, err := b.Validate(); err == nil {
		t.Fatal("empty open bundle accepted")
	}
	b.Sub = id
	b.Revision = 2
	if _, err := b.Validate(); err == nil {
		t.Fatal("empty open subscription accepted")
	}
	b.Closed = true
	b.Mode = "merge"
	if _, err := b.Validate(); err == nil {
		t.Fatal("closed merge accepted")
	}
}
