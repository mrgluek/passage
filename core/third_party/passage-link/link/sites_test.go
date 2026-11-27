package link

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func sitesBundle() Bundle { return referencesBundle() }

func TestBundleSitesRoundTrip(t *testing.T) {
	b := sitesBundle()
	b.Sites = []SiteGroup{{Via: "smart", Hosts: []string{"example.org", "a.example.net"}}, {Via: "tunnel", Hosts: []string{"example.com"}}}
	encoded, err := Encode(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Decode(encoded, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Bundle.Sites) != 2 || d.Bundle.Sites[0].Hosts[1] != "a.example.net" || d.Bundle.Sites[1].Via != "tunnel" {
		t.Fatalf("sites lost: %v", d.Bundle.Sites)
	}
	again, err := Encode(d.Bundle, nil)
	if err != nil || !bytes.Equal(encoded, again) {
		t.Fatal("round trip changed", err)
	}
	old, err := Encode(sitesBundle(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := Decode(old, nil); err != nil || len(d.Bundle.Sites) != 0 {
		t.Fatalf("bundle without sites: %v %v", d.Bundle.Sites, err)
	}
}

// An app that does not know the key must still accept the bundle.
func TestSitesKeyIgnoredByOldDecoder(t *testing.T) {
	b := sitesBundle()
	b.Sites = []SiteGroup{{Via: "smart", Hosts: []string{"example.org"}}}
	raw, err := cbor.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	type oldBundle struct {
		V    uint64 `cbor:"v"`
		Kind string `cbor:"kind"`
	}
	var o oldBundle
	if err := cbor.Unmarshal(raw, &o); err != nil || o.Kind != "routes" {
		t.Fatal(err)
	}
}

func TestSitesLimits(t *testing.T) {
	hosts := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("h%d.example.org", i)
		}
		return out
	}
	b := sitesBundle()
	b.Sites = []SiteGroup{{Via: "smart", Hosts: hosts(MaxSiteHosts)}}
	if _, err := Encode(b, nil); err != nil {
		t.Fatalf("120 hosts: %v", err)
	}
	b.Sites = []SiteGroup{{Via: "smart", Hosts: hosts(MaxSiteHosts + 1)}}
	if _, err := Encode(b, nil); err == nil {
		t.Fatal("121 hosts accepted")
	}
	b.Sites = nil
	for i := 0; i < MaxSiteGroups; i++ {
		b.Sites = append(b.Sites, SiteGroup{Via: "smart", Hosts: []string{"example.org"}})
	}
	if _, err := Encode(b, nil); err != nil {
		t.Fatalf("16 groups: %v", err)
	}
	b.Sites = append(b.Sites, SiteGroup{Via: "smart", Hosts: []string{"example.org"}})
	if _, err := Encode(b, nil); err == nil {
		t.Fatal("17 groups accepted")
	}
	b.Sites = []SiteGroup{{Via: "smart"}}
	if _, err := Encode(b, nil); err == nil {
		t.Fatal("empty group accepted")
	}
}

func TestSitesBadHosts(t *testing.T) {
	for _, h := range []string{"", "Example.org", "*.example.org", ".example.org", "example.org.", "localhost", "org", "co.uk", "10.0.0.1", "1.2.3.4", "[::1]", "a b.example.org", "example.org:443", "exa_mple.org", "https://example.org", "-a.example.org", strings.Repeat("a", 64) + ".example.org", "github.io"} {
		if err := ValidateSiteHost(h); err == nil {
			t.Errorf("%q accepted", h)
		}
	}
	for _, h := range []string{"example.org", "a.b.example.org", "xn--e1afmkfd.xn--p1ai", "my-site.example.net"} {
		if err := ValidateSiteHost(h); err != nil {
			t.Errorf("%q: %v", h, err)
		}
	}
}

func TestSitesReservedAndUnknownKinds(t *testing.T) {
	b := sitesBundle()
	b.Sites = []SiteGroup{{Via: "ru", Hosts: []string{"example.org"}}}
	if _, err := Encode(b, nil); err != nil {
		t.Fatalf("reserved ru refused: %v", err)
	}
	if err := ValidateSitesStrict(b.Sites); err != nil {
		t.Fatal(err)
	}
	b.Sites = []SiteGroup{{Via: "future", Hosts: []string{"example.org"}}}
	if _, err := Encode(b, nil); err != nil {
		t.Fatalf("unknown kind must decode: %v", err)
	}
	if ValidateSitesStrict(b.Sites) == nil {
		t.Fatal("publisher accepted unknown kind")
	}
	b.Sites = []SiteGroup{{Via: "Smart!", Hosts: []string{"example.org"}}}
	if _, err := Encode(b, nil); err == nil {
		t.Fatal("malformed kind accepted")
	}
}

func TestClosedSubscriptionRefusesSites(t *testing.T) {
	b := sitesBundle()
	b.Closed = true
	b.Routes = nil
	b.Sites = []SiteGroup{{Via: "smart", Hosts: []string{"example.org"}}}
	if _, err := Encode(b, nil); err == nil {
		t.Fatal("closed bundle with sites accepted")
	}
}
