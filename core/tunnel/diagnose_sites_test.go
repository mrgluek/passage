package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNetworkTestChecksListedSitesUnderLabels(t *testing.T) {
	down := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("filtered") }
	tt := newBrowserTunnel(t, testSites, down, down)
	tt.cfg.Canaries, tt.cfg.References = nil, nil
	saved := diagTelegramDCs
	diagTelegramDCs = nil
	defer func() { diagTelegramDCs = saved }()
	var mu sync.Mutex
	probed := map[string]string{} // address -> SNI, direct column
	tt.probeTLS = func(_ context.Context, addr, sni string) error {
		mu.Lock()
		probed[addr] = sni
		mu.Unlock()
		return nil
	}
	var tunnelProbes atomic.Int32
	tt.ways[RouteTunnel] = way{func(context.Context, string, string) (net.Conn, error) {
		tunnelProbes.Add(1)
		return nil, errors.New("down")
	}, 0}
	d := tt.Diagnose(context.Background())
	var sites []DiagRow
	for _, r := range d.Rows {
		if r.Kind == "site" {
			sites = append(sites, r)
		}
	}
	if len(sites) != 2 {
		t.Fatalf("%d site rows: %+v", len(sites), d.Rows)
	}
	out, _ := json.Marshal(d)
	if strings.Contains(string(out), "example.org") || strings.Contains(string(out), "example.net") {
		t.Fatalf("site names in the test result: %s", out)
	}
	for _, r := range sites {
		if !strings.HasPrefix(r.Target, "site #") || r.Site == 0 || r.Host == "" || !r.Results[0].OK {
			t.Fatalf("%+v", r)
		}
	}
	// Direct dials the DoT address with the site's name as SNI.
	mu.Lock()
	if probed["192.0.2.1:443"] == "" {
		t.Fatalf("direct probes %v", probed)
	}
	mu.Unlock()
	// The smart group skips the tunnel column, the tunnel group uses it.
	tunnelCol := len(d.Ways) - 1
	smart, tunnel := sites[0], sites[1]
	if strings.Contains(smart.Target, "(tunnel)") {
		smart, tunnel = tunnel, smart
	}
	if !smart.Results[tunnelCol].Skip || tunnel.Results[tunnelCol].Skip || tunnelProbes.Load() != 1 {
		t.Fatalf("smart %+v tunnel %+v probes %d", smart.Results, tunnel.Results, tunnelProbes.Load())
	}
	// The number is the place in the list as subscribed: example.org first.
	if smart.Target != "site #1 (smart)" || smart.Host != "example.org" || tunnel.Target != "site #2 (tunnel)" {
		t.Fatalf("%q %q %q", smart.Target, smart.Host, tunnel.Target)
	}
}

// Numbers follow the subscription's order, not the sorted list, and stay
// the same for the same list.
func TestSiteNumbersFollowTheSubscriptionOrder(t *testing.T) {
	groups := []SiteGroup{{Via: "tunnel", Hosts: []string{"zeta.example.org", "alpha.example.org"}}, {Via: "smart", Hosts: []string{"mid.example.org", "alpha.example.org"}}}
	for range 2 {
		tt := newBrowserTunnel(t, groups, nil, nil)
		for host, want := range map[string]string{"zeta.example.org": "site #1 (tunnel)", "alpha.example.org": "site #2 (tunnel)", "mid.example.org": "site #3 (smart)"} {
			if got := tt.sites.match(host).label(); got != want {
				t.Fatalf("%s: %q, want %q", host, got, want)
			}
		}
	}
}

// Every listed site is tested, at most diagSiteRows of them; the rest is counted.
func TestNetworkTestCapsTheSiteRows(t *testing.T) {
	var hosts []string
	for i := range diagSiteRows + 3 {
		hosts = append(hosts, fmt.Sprintf("s%02d.example.org", i))
	}
	down := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("filtered") }
	tt := newBrowserTunnel(t, []SiteGroup{{Via: "smart", Hosts: hosts}}, down, down)
	tt.cfg.Canaries, tt.cfg.References = nil, nil
	saved := diagTelegramDCs
	diagTelegramDCs = nil
	defer func() { diagTelegramDCs = saved }()
	tt.probeTLS = func(context.Context, string, string) error { return nil }
	d := tt.Diagnose(context.Background())
	n := 0
	for _, r := range d.Rows {
		if r.Kind == "site" {
			n++
		}
	}
	if n != diagSiteRows || d.SitesNotTested != 3 {
		t.Fatalf("%d rows, %d not tested", n, d.SitesNotTested)
	}
}
