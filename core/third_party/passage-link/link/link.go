// Package link encodes and validates portable Passage route collections.
package link

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/fxamacker/cbor/v2"
)

const MaxBytes = 65536
const MaxRoutes = 6
const MaxUpdates = 4
const CompactBytes = 1024

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
var enc cbor.EncMode
var dec cbor.DecMode
var generic cbor.DecMode

func init() {
	var err error
	enc, err = cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	dec, err = (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, IndefLength: cbor.IndefLengthForbidden, MaxNestedLevels: 16, MaxArrayElements: 128, MaxMapPairs: 128, DefaultMapType: reflect.TypeOf(map[string]any{})}).DecMode()
	if err != nil {
		panic(err)
	}
	generic, err = (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, IndefLength: cbor.IndefLengthForbidden, MaxNestedLevels: 16, MaxArrayElements: 128, MaxMapPairs: 128}).DecMode()
	if err != nil {
		panic(err)
	}
}

type Route struct {
	ID       string         `json:"id" cbor:"id"`
	Type     string         `json:"type" cbor:"type"`
	Wire     string         `json:"wire" cbor:"wire"`
	Priority uint16         `json:"priority" cbor:"priority"`
	IPv6     bool           `json:"ipv6,omitempty" cbor:"ipv6,omitempty"`
	Params   map[string]any `json:"params" cbor:"params"`
}
type Bundle struct {
	V           uint64   `json:"v" cbor:"v"`
	Kind        string   `json:"kind" cbor:"kind"`
	Mode        string   `json:"mode" cbor:"mode"`
	Name        string   `json:"name,omitempty" cbor:"name,omitempty"`
	IssuedAt    uint64   `json:"issued_at,omitempty" cbor:"issued_at,omitempty"`
	ReviewAfter uint64   `json:"review_after,omitempty" cbor:"review_after,omitempty"`
	Signer      []byte   `json:"signer,omitempty" cbor:"signer,omitempty"`
	Routes      []Route  `json:"routes" cbor:"routes"`
	Required    []string `json:"required,omitempty" cbor:"required,omitempty"`
	// Updates lists more addresses where app releases are published.
	Updates []string `json:"updates,omitempty" cbor:"updates,omitempty"`
}
type Decoded struct {
	Bundle      Bundle   `json:"bundle"`
	Signature   string   `json:"signature"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Unsupported []string `json:"unsupported,omitempty"`
}
type SoarParams struct {
	Zone      string   `json:"zone"`
	Key       []byte   `json:"key"`
	Resolvers []string `json:"resolvers,omitempty"`
	Canaries  []string `json:"canaries,omitempty"`
}
type SocksAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type WSSParams struct {
	Endpoint string `json:"endpoint"`
	Key      []byte `json:"key"`
}

type HTTPParams struct {
	Endpoint string `json:"endpoint"`
	Key      []byte `json:"key"`
	DialIP   string `json:"dial_ip,omitempty"`
}

type IrohParams struct {
	Ticket string    `json:"ticket"`
	Relay  string    `json:"relay"`
	Socks  SocksAuth `json:"socks"`
}

func ValidID(s string) bool         { return idPattern.MatchString(s) }
func Fingerprint(pub []byte) string { sum := sha256.Sum256(pub); return fmt.Sprintf("%x", sum[:]) }
func canonical(data []byte, out any) error {
	if len(data) == 0 || len(data) > MaxBytes {
		return errors.New("CBOR size limit")
	}
	var value any
	if err := generic.Unmarshal(data, &value); err != nil {
		return errors.New("invalid CBOR")
	}
	if err := plain(value); err != nil {
		return err
	}
	encoded, err := enc.Marshal(value)
	if err != nil || !bytes.Equal(data, encoded) {
		return errors.New("non-deterministic CBOR")
	}
	if err := dec.Unmarshal(data, out); err != nil {
		return errors.New("invalid CBOR shape")
	}
	return nil
}
func plain(v any) error {
	switch x := v.(type) {
	case float32, float64, cbor.Tag:
		return errors.New("unsupported CBOR value")
	case []any:
		for _, v := range x {
			if err := plain(v); err != nil {
				return err
			}
		}
	case map[any]any:
		for k, v := range x {
			switch k.(type) {
			case string, uint64, int64:
			default:
				return errors.New("invalid map key")
			}
			if err := plain(v); err != nil {
				return err
			}
		}
	}
	return nil
}
func JSON(data []byte, out any) error {
	if len(data) > MaxBytes || !utf8.Valid(data) {
		return errors.New("JSON size limit")
	}
	check := json.NewDecoder(bytes.NewReader(data))
	check.UseNumber()
	if err := jsonValue(check, 0); err != nil {
		return err
	}
	if _, err := check.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid JSON fields or types")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
func jsonValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("JSON nesting limit")
	}
	token, err := d.Token()
	if err != nil {
		return errors.New("invalid JSON")
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return errors.New("invalid JSON")
			}
			text, ok := key.(string)
			if !ok || seen[text] {
				return errors.New("duplicate JSON key")
			}
			seen[text] = true
			if len(seen) > 128 {
				return errors.New("JSON map limit")
			}
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON")
		}
	case json.Delim('['):
		n := 0
		for d.More() {
			n++
			if n > 128 {
				return errors.New("JSON array limit")
			}
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON")
		}
	default:
		if number, ok := token.(json.Number); ok && strings.ContainsAny(string(number), ".eE") {
			return errors.New("non-integer JSON number")
		}
		if _, ok := token.(json.Delim); ok {
			return errors.New("invalid JSON")
		}
	}
	return nil
}
func Params[T any](r Route) (T, error) {
	var out T
	b, err := json.Marshal(r.Params)
	if err != nil {
		return out, errors.New("invalid parameters")
	}
	err = JSON(b, &out)
	return out, err
}
func validateHostPort(s string, ipOnly bool) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return errors.New("invalid host:port")
	}
	var n int
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil || fmt.Sprint(n) != port || n < 1 || n > 65535 {
		return errors.New("invalid port")
	}
	if ipOnly && net.ParseIP(host) == nil {
		return errors.New("resolver must be an IP address")
	}
	return nil
}
func ValidateRoute(r Route) (bool, error) {
	if !ValidID(r.ID) || len(r.Type) > 48 || len(r.Wire) > 96 || r.Type == "" || r.Wire == "" || r.Params == nil {
		return false, errors.New("invalid route fields")
	}
	switch r.Type + "/" + r.Wire {
	case "soar/soar-v1":
		p, err := Params[SoarParams](r)
		if err != nil {
			return true, err
		}
		if len(p.Key) != 32 || len(p.Zone) > 200 || !validZone(p.Zone) || len(p.Resolvers) > 64 || len(p.Canaries) > 8 {
			return true, errors.New("invalid DNS route parameters")
		}
		for _, v := range p.Resolvers {
			if err := validateHostPort(v, true); err != nil {
				return true, err
			}
		}
		for _, v := range p.Canaries {
			if err := validateHostPort(v, false); err != nil {
				return true, err
			}
		}
	case "http/http-v1":
		p, err := Params[HTTPParams](r)
		if err != nil {
			return true, err
		}
		if p.DialIP != "" && net.ParseIP(p.DialIP) == nil {
			return true, errors.New("invalid HTTP dial IP")
		}
		if len(p.Endpoint) > 1024 || !strings.HasPrefix(p.Endpoint, "https://") {
			return true, errors.New("invalid HTTP endpoint")
		}
		copy := r
		copy.Type, copy.Wire = "wss", "wss-v1"
		copy.Params = map[string]any{"endpoint": "wss://" + strings.TrimPrefix(p.Endpoint, "https://"), "key": p.Key}
		return ValidateRoute(copy)
	case "wss/wss-v1":
		p, err := Params[WSSParams](r)
		if err != nil {
			return true, err
		}
		u, err := url.Parse(p.Endpoint)
		if err != nil || len(p.Endpoint) > 2048 || u.Scheme != "wss" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(p.Endpoint, "#") || strings.ContainsAny(p.Endpoint, "\\\r\n\t ") || strings.Contains(u.Hostname(), "%") {
			return true, errors.New("invalid WSS endpoint")
		}
		host := u.Hostname()
		if net.ParseIP(host) == nil && !validZone(strings.ToLower(host)) {
			return true, errors.New("invalid WSS endpoint host")
		}
		if strings.HasSuffix(u.Host, ":") {
			return true, errors.New("invalid WSS endpoint port")
		}
		if u.Port() != "" {
			if err := validateHostPort(u.Host, false); err != nil {
				return true, err
			}
		}
		if len(p.Key) != 32 {
			return true, errors.New("invalid WSS key")
		}
	case "iroh/dumbpipe-v0/iroh-0.35":
		p, err := Params[IrohParams](r)
		if err != nil {
			return true, err
		}
		if err := validateTicket(p.Ticket, p.Relay); err != nil {
			return true, err
		}
		u, err := url.Parse(p.Relay)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Port() != "" && u.Port() != "443" {
			return true, errors.New("invalid relay URL")
		}
		if len(p.Socks.Username) < 1 || len(p.Socks.Username) > 255 || len(p.Socks.Password) < 1 || len(p.Socks.Password) > 255 {
			return true, errors.New("invalid SOCKS credentials")
		}
	default:
		return false, nil
	}
	return true, nil
}
func validZone(zone string) bool {
	if len(zone) < 3 || !strings.Contains(zone, ".") || zone != strings.ToLower(zone) {
		return false
	}
	for _, label := range strings.Split(zone, ".") {
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
func (b Bundle) Validate() ([]string, error) {
	if b.V != 2 || b.Kind != "routes" || b.Mode != "merge" && b.Mode != "replace" || len(b.Name) > 128 || len(b.Routes) < 1 || len(b.Routes) > MaxRoutes {
		return nil, errors.New("invalid route collection")
	}
	if len(b.Required) > 0 {
		return nil, errors.New("unsupported required feature: update Passage")
	}
	if b.ReviewAfter != 0 && b.ReviewAfter < b.IssuedAt {
		return nil, errors.New("invalid review date")
	}
	if len(b.Updates) > MaxUpdates {
		return nil, errors.New("too many update addresses")
	}
	for _, s := range b.Updates {
		if u, err := url.Parse(s); err != nil || len(s) > 256 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid update address")
		}
	}
	ids := map[string]bool{}
	unsupported := []string{}
	usable := 0
	for _, r := range b.Routes {
		if ids[r.ID] {
			return nil, errors.New("duplicate route ID")
		}
		ids[r.ID] = true
		supported, err := ValidateRoute(r)
		if err != nil {
			return nil, fmt.Errorf("route %s: %w", r.ID, err)
		}
		if supported {
			usable++
		} else {
			unsupported = append(unsupported, r.ID)
		}
	}
	if usable == 0 {
		return unsupported, errors.New("no supported routes: update Passage")
	}
	return unsupported, nil
}
func Encode(b Bundle, key ed25519.PrivateKey) ([]byte, error) {
	if len(key) != 0 && len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid signing key")
	}
	if len(key) == 0 {
		return EncodeWithSigner(b, nil)
	}
	return EncodeWithSigner(b, key)
}

// EncodeWithSigner keeps private-key storage outside the wire codec.
func EncodeWithSigner(b Bundle, signer crypto.Signer) ([]byte, error) {
	b.Signer = nil
	if signer != nil {
		public, ok := signer.Public().(ed25519.PublicKey)
		if !ok || len(public) != ed25519.PublicKeySize {
			return nil, errors.New("signer must use Ed25519")
		}
		b.Signer = append([]byte(nil), public...)
	}
	if _, err := b.Validate(); err != nil {
		return nil, err
	}
	body, err := enc.Marshal(b)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return body, nil
	}
	kid := sha256.Sum256(b.Signer)
	protected, _ := enc.Marshal(map[int]any{1: int64(-19), 4: kid[:]})
	toSign, _ := enc.Marshal([]any{"Signature1", protected, []byte("passage-link-v2"), body})
	signature, err := signer.Sign(rand.Reader, toSign, crypto.Hash(0))
	if err != nil {
		return nil, errors.New("signer refused signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(b.Signer), toSign, signature) {
		return nil, errors.New("signer returned an invalid signature")
	}
	raw, err := enc.Marshal(cbor.Tag{Number: 18, Content: []any{protected, map[string]any{}, body, signature}})
	if len(raw) > MaxBytes {
		return nil, errors.New("link size limit")
	}
	return raw, err
}
func Decode(raw []byte, trusted []ed25519.PublicKey) (result Decoded, err error) {
	defer func() {
		if err != nil {
			result = Decoded{}
		}
	}()
	if len(raw) == 0 || len(raw) > MaxBytes {
		return result, errors.New("link size limit")
	}
	var body []byte
	var signer []byte
	if raw[0] == 0xd2 {
		var tag cbor.RawTag
		if err := dec.Unmarshal(raw, &tag); err != nil || tag.Number != 18 {
			return result, errors.New("invalid signature envelope")
		}
		var parts []cbor.RawMessage
		if err := canonical(tag.Content, &parts); err != nil || len(parts) != 4 {
			return result, errors.New("invalid signature envelope")
		}
		var protected, signature []byte
		var unprotected map[string]any
		if dec.Unmarshal(parts[0], &protected) != nil || dec.Unmarshal(parts[1], &unprotected) != nil || len(unprotected) != 0 || dec.Unmarshal(parts[2], &body) != nil || dec.Unmarshal(parts[3], &signature) != nil {
			return result, errors.New("invalid signature fields")
		}
		var headers map[int]any
		if err := canonical(protected, &headers); err != nil || len(headers) != 2 {
			return result, errors.New("invalid signature headers")
		}
		if headers[1] != int64(-19) {
			return result, errors.New("unsupported signature algorithm")
		}
		if err := canonical(body, &result.Bundle); err != nil {
			return result, err
		}
		signer = result.Bundle.Signer
		kid := sha256.Sum256(signer)
		given, ok := headers[4].([]byte)
		if !ok || !bytes.Equal(kid[:], given) || len(signer) != 32 || len(signature) != 64 {
			return result, errors.New("invalid signer")
		}
		toSign, _ := enc.Marshal([]any{"Signature1", protected, []byte("passage-link-v2"), body})
		if !ed25519.Verify(ed25519.PublicKey(signer), toSign, signature) {
			return result, errors.New("invalid signature")
		}
		result.Signature = "unknown_signer"
		result.Fingerprint = Fingerprint(signer)
		for _, pub := range trusted {
			if bytes.Equal(pub, signer) {
				result.Signature = "trusted_author"
				break
			}
		}
	} else {
		if err := canonical(raw, &result.Bundle); err != nil {
			return result, err
		}
		if len(result.Bundle.Signer) != 0 {
			return result, errors.New("unsigned collection contains a signer")
		}
		result.Signature = "unsigned"
	}
	unsupported, err := result.Bundle.Validate()
	result.Unsupported = unsupported
	return result, err
}
func URI(raw []byte, webBase string) (string, error) {
	prefix := "passage://add"
	if webBase != "" {
		u, err := url.Parse(webBase)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return "", errors.New("invalid HTTPS wrapper")
		}
		prefix = u.String()
	}
	s := prefix + "#v2." + base64.RawURLEncoding.EncodeToString(raw)
	if len(s) > 16384 {
		return "", errors.New("text link exceeds 16 KiB; export a file")
	}
	return s, nil
}
func ParseURI(s string, trusted []ed25519.PublicKey) (Decoded, error) {
	if len(s) > 16384 {
		return Decoded{}, errors.New("text link size limit")
	}
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return Decoded{}, errors.New("invalid link")
	}
	if u.User != nil || u.RawQuery != "" || u.Scheme != "https" && !(u.Scheme == "passage" && u.Host == "add" && u.Path == "") || u.Scheme == "https" && u.Hostname() == "" {
		return Decoded{}, errors.New("invalid link envelope")
	}
	f := u.Fragment
	if !strings.HasPrefix(f, "v2.") {
		return Decoded{}, errors.New("not a v2 link")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(f, "v2."))
	if err != nil {
		return Decoded{}, errors.New("invalid link encoding")
	}
	return Decode(raw, trusted)
}
func ImportV1(s, id string) (Route, error) {
	r := Route{ID: id, Type: "soar", Wire: "soar-v1", Priority: 100}
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return r, errors.New("invalid v1 link")
	}
	var raw string
	switch {
	case (u.Scheme == "passage" || u.Scheme == "velta") && u.Host == "tunnel":
		raw = u.RawQuery
	case u.Scheme == "https" && strings.TrimSuffix(u.Path, "/") == "/link" && u.User == nil:
		raw = u.EscapedFragment()
	default:
		return r, errors.New("unsupported v1 envelope")
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return r, errors.New("invalid v1 fields")
	}
	for _, v := range q {
		if len(v) != 1 {
			return r, errors.New("duplicate v1 field")
		}
	}
	if q.Get("v") != "" && q.Get("v") != "1" {
		return r, errors.New("unsupported link version")
	}
	text := strings.ReplaceAll(q.Get("key"), " ", "+")
	var key []byte
	for _, e := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err = e.DecodeString(text); err == nil {
			break
		}
	}
	if err != nil || len(key) != 32 {
		return r, errors.New("invalid server key")
	}
	p := SoarParams{Zone: strings.TrimSuffix(strings.ToLower(q.Get("zone")), "."), Key: key}
	if q.Get("r") != "" {
		p.Resolvers = strings.Split(q.Get("r"), ",")
		for i, s := range p.Resolvers {
			if net.ParseIP(s) != nil {
				p.Resolvers[i] = net.JoinHostPort(s, "53")
			}
		}
	}
	if q.Get("c") != "" {
		p.Canaries = strings.Split(q.Get("c"), ",")
		for i, s := range p.Canaries {
			if !strings.Contains(s, ":") {
				p.Canaries[i] = net.JoinHostPort(s, "443")
			}
		}
	}
	if q.Get("ipv6") != "" && q.Get("ipv6") != "0" && q.Get("ipv6") != "1" {
		return r, errors.New("invalid IPv6 flag")
	}
	r.IPv6 = q.Get("ipv6") == "1"
	b, _ := json.Marshal(p)
	json.Unmarshal(b, &r.Params)
	_, err = ValidateRoute(r)
	return r, err
}
func Sorted(routes []Route) []Route {
	out := append([]Route(nil), routes...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Apply validates before changing anything. Collision replacement is explicit.
func Apply(existing []Route, incoming Bundle, replaceIDs bool) ([]Route, error) {
	if _, err := incoming.Validate(); err != nil {
		return nil, err
	}
	out := []Route{}
	if incoming.Mode == "merge" {
		out = append(out, existing...)
	}
	for _, r := range incoming.Routes {
		supported, _ := ValidateRoute(r)
		if !supported {
			continue
		}
		found := -1
		for i, x := range out {
			if x.ID == r.ID {
				found = i
				break
			}
		}
		if found >= 0 {
			a, _ := enc.Marshal(out[found])
			b, _ := enc.Marshal(r)
			if bytes.Equal(a, b) {
				continue
			}
			if !replaceIDs {
				return nil, errors.New("route ID collision requires confirmation")
			}
			out[found] = r
		} else {
			out = append(out, r)
		}
	}
	if len(out) > 32 {
		return nil, errors.New("installed route limit")
	}
	return Sorted(out), nil
}
func RouteHash(r Route) string {
	raw, _ := enc.Marshal(r)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}
