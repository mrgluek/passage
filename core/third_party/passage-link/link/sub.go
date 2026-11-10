package link

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/fxamacker/cbor/v2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const MaxSubRoutes = 16
const MaxSubFileBytes = 16384
const MaxSubSources = 4
const SubFileOverhead = 5 + chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead

var subBuckets = [...]int{1024, 2048, 4096, 8192, 16384}
var ErrNotNewer = errors.New("subscription unchanged")
var ErrRollback = errors.New("subscription revision rollback")

type Invite struct {
	V        uint64   `json:"v" cbor:"v"`
	Kind     string   `json:"kind" cbor:"kind"`
	ID       []byte   `json:"id" cbor:"id"`
	Name     string   `json:"name" cbor:"name"`
	Sources  []string `json:"sources" cbor:"sources"`
	ReadKey  []byte   `json:"read_key" cbor:"read_key"`
	Seed     []byte   `json:"seed,omitempty" cbor:"seed,omitempty"`
	Required []string `json:"required" cbor:"required"`
	Signer   []byte   `json:"signer" cbor:"signer"`
}

// UnmarshalCBOR rejects missing, null and unknown invite fields.
func (i *Invite) UnmarshalCBOR(raw []byte) error {
	var fields map[string]cbor.RawMessage
	if err := dec.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range []string{"v", "kind", "id", "name", "sources", "read_key", "required", "signer"} {
		if _, ok := fields[key]; !ok {
			return errors.New("missing invite field")
		}
	}
	for key, value := range fields {
		switch key {
		case "v", "kind", "id", "name", "sources", "read_key", "required", "signer", "seed":
		default:
			return errors.New("unknown invite field")
		}
		if bytes.Equal(value, []byte{0xf6}) || bytes.Equal(value, []byte{0xf7}) {
			return errors.New("null invite field")
		}
	}
	type wire Invite
	var out wire
	if err := dec.Unmarshal(raw, &out); err != nil {
		return err
	}
	*i = Invite(out)
	return nil
}

type DecodedInvite struct {
	Invite      Invite `json:"invite"`
	Signature   string `json:"signature"`
	Fingerprint string `json:"fingerprint"`
}

func (i Invite) Validate() error {
	if i.V != 2 || i.Kind != "sub" || len(i.ID) != 16 || len(i.ReadKey) != 32 || len(i.Signer) != 32 || len(i.Name) > 128 || !utf8.ValidString(i.Name) || len(i.Sources) < 1 || len(i.Sources) > MaxSubSources || len(i.Required) != 1 || i.Required[0] != "sub" {
		return errors.New("invalid subscription invite")
	}
	if err := ValidateSources(i.Sources); err != nil {
		return err
	}
	if i.Seed != nil {
		if _, err := OpenSubFile(i.Seed, i.ID, i.ReadKey, ed25519.PublicKey(i.Signer)); err != nil {
			return err
		}
	}
	return nil
}
func validSubDNS(host string) bool {
	if len(host) > 253 {
		return false
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func EncodeInvite(i Invite, signer crypto.Signer) ([]byte, error) {
	if signer == nil {
		return nil, errors.New("invite requires signer")
	}
	pub, ok := signer.Public().(ed25519.PublicKey)
	if !ok || len(pub) != 32 {
		return nil, errors.New("signer must use Ed25519")
	}
	i.Signer = append([]byte(nil), pub...)
	if err := i.Validate(); err != nil {
		return nil, err
	}
	body, err := enc.Marshal(i)
	if err != nil {
		return nil, err
	}
	return signBody(body, i.Signer, signer)
}

// DecodeInvite accepts raw CBOR or a native/HTTPS text wrapper.
func DecodeInvite(raw []byte, trusted []ed25519.PublicKey) (DecodedInvite, error) {
	text := bytes.TrimSpace(raw)
	if bytes.HasPrefix(text, []byte("passage:")) || bytes.HasPrefix(text, []byte("https:")) {
		if len(raw) > MaxBytes {
			return DecodedInvite{}, errors.New("invite text size limit")
		}
		u, err := url.Parse(string(text))
		if err != nil || u.User != nil || u.ForceQuery || u.RawQuery != "" || (u.Scheme != "https" && !(u.Scheme == "passage" && u.Host == "add" && u.Path == "")) || (u.Scheme == "https" && u.Hostname() == "") || !strings.HasPrefix(u.Fragment, "v2.") {
			return DecodedInvite{}, errors.New("invalid invite envelope")
		}
		raw, err = base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(u.Fragment, "v2."))
		if err != nil {
			return DecodedInvite{}, errors.New("invalid invite encoding")
		}
	}
	var i Invite
	d, err := decodeEnvelope(raw, trusted, &i)
	if err != nil {
		return DecodedInvite{}, err
	}
	if d.Signature == "unsigned" {
		return DecodedInvite{}, errors.New("unsigned invite")
	}
	if err = i.Validate(); err != nil {
		return DecodedInvite{}, err
	}
	return DecodedInvite{i, d.Signature, d.Fingerprint}, nil
}

// InviteURI uses the larger text allowance for an embedded subscription seed.
func InviteURI(raw []byte, webBase string) (string, error) { return uri(raw, webBase, MaxBytes) }
func subInputs(id, key []byte) error {
	if len(id) != 16 || len(key) != 32 {
		return errors.New("invalid subscription id or read key")
	}
	return nil
}

// SealClosedSubFile signs and encrypts a final subscription revision.
func SealClosedSubFile(id, readKey []byte, revision uint64, signer crypto.Signer) ([]byte, error) {
	return SealSubFile(Bundle{V: 2, Kind: "routes", Mode: "replace", Sub: id, Revision: revision, Closed: true, Routes: []Route{}}, id, readKey, signer)
}

func SealSubFile(b Bundle, id, readKey []byte, signer crypto.Signer) ([]byte, error) {
	return sealSubFile(b, id, readKey, signer, rand.Reader)
}
func sealSubFile(b Bundle, id, readKey []byte, signer crypto.Signer, random io.Reader) ([]byte, error) {
	if err := subInputs(id, readKey); err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("subscription requires signer")
	}
	if !bytes.Equal(b.Sub, id) {
		return nil, errors.New("subscription id mismatch")
	}
	raw, err := EncodeWithSigner(b, signer)
	if err != nil {
		return nil, err
	}
	bucket := 0
	for _, n := range subBuckets {
		if len(raw)+4 <= n {
			bucket = n
			break
		}
	}
	if bucket == 0 {
		return nil, errors.New("subscription bundle exceeds largest padding bucket")
	}
	plain := make([]byte, bucket)
	binary.BigEndian.PutUint32(plain, uint32(len(raw)))
	copy(plain[4:], raw)
	aead, _ := chacha20poly1305.NewX(readKey)
	header := make([]byte, 29)
	copy(header, "PSUB1")
	if _, err = io.ReadFull(random, header[5:]); err != nil {
		return nil, err
	}
	return aead.Seal(header, header[5:], plain, append([]byte("PSUB1"), id...)), nil
}
func OpenSubFile(blob, id, readKey []byte, signer ed25519.PublicKey) (Decoded, error) {
	fail := func() (Decoded, error) { return Decoded{}, errors.New("invalid subscription file") }
	if subInputs(id, readKey) != nil || len(signer) != 32 || len(blob) > MaxSubFileBytes+SubFileOverhead || len(blob) < SubFileOverhead || string(blob[:5]) != "PSUB1" {
		return fail()
	}
	bucket := len(blob) - SubFileOverhead
	valid := false
	for _, n := range subBuckets {
		valid = valid || bucket == n
	}
	if !valid {
		return fail()
	}
	aead, _ := chacha20poly1305.NewX(readKey)
	plain, err := aead.Open(nil, blob[5:29], blob[29:], append([]byte("PSUB1"), id...))
	if err != nil {
		return fail()
	}
	n := int(binary.BigEndian.Uint32(plain))
	if n < 1 || n > len(plain)-4 {
		return fail()
	}
	for _, v := range plain[4+n:] {
		if v != 0 {
			return fail()
		}
	}
	d, err := Decode(plain[4:4+n], []ed25519.PublicKey{signer})
	if err != nil {
		return Decoded{}, err
	}
	if d.Signature != "trusted_author" || !bytes.Equal(d.Bundle.Sub, id) || d.Bundle.Revision == 0 || d.Bundle.Mode != "replace" {
		return fail()
	}
	return d, nil
}
func CheckRevision(stored, got uint64) error {
	if got == 0 || got < stored {
		return ErrRollback
	}
	if got == stored {
		return ErrNotNewer
	}
	return nil
}
func DeriveReadKey(signer crypto.Signer, id []byte) ([]byte, error) {
	if signer == nil || len(id) != 16 {
		return nil, errors.New("invalid subscription signer or id")
	}
	pub, ok := signer.Public().(ed25519.PublicKey)
	if !ok || len(pub) != 32 {
		return nil, errors.New("signer must use Ed25519")
	}
	msg := append([]byte("passage-sub-read-v1\n"), id...)
	a, err := signer.Sign(rand.Reader, msg, crypto.Hash(0))
	if err != nil {
		return nil, errors.New("read key signing failed")
	}
	b, err := signer.Sign(rand.Reader, msg, crypto.Hash(0))
	if err != nil || !bytes.Equal(a, b) {
		return nil, errors.New("read key signer is not deterministic")
	}
	if !ed25519.Verify(pub, msg, a) {
		return nil, errors.New("invalid read key signature")
	}
	key := make([]byte, 32)
	_, err = io.ReadFull(hkdf.New(sha256.New, a, id, []byte("passage-sub-read-v1")), key)
	return key, err
}

// ValidateSources checks optional subscription file base addresses.
func ValidateSources(sources []string) error {
	if len(sources) > MaxSubSources {
		return errors.New("too many subscription sources")
	}
	for _, s := range sources {
		u, e := url.Parse(s)
		if e != nil || len(s) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(s, "#") || strings.Contains(s, "\\") || u.Opaque != "" {
			return errors.New("invalid subscription source")
		}
		h := u.Hostname()
		if net.ParseIP(h) == nil && (strings.HasPrefix(u.Host, "[") || !validSubDNS(h)) {
			return errors.New("invalid subscription host")
		}
		if strings.HasSuffix(u.Host, ":") {
			return errors.New("invalid subscription port")
		}
		if u.Port() != "" {
			if e := validateHostPort(u.Host, false); e != nil {
				return e
			}
		}
	}
	return nil
}
