package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	var tunnelProbes atomic.Int32
	tunnelDial := func(context.Context, string, string) (net.Conn, error) {
		tunnelProbes.Add(1)
		return nil, errors.New("down")
	}
	tt.ways[RouteTunnel] = way{tunnelDial, 0}
	tt.siteDataProbe = func(ctx context.Context, dial dialFunc, addr, sni string) error {
		if c, err := dial(ctx, "tcp", addr); err != nil {
			return err
		} else {
			c.Close()
		}
		mu.Lock()
		probed[addr] = sni
		mu.Unlock()
		return nil
	}
	tt.ways[RouteDirect] = way{echoDialer("d:", new(atomic.Int32)), time.Second}
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
	tt.siteDataProbe = func(context.Context, dialFunc, string, string) error { return nil }
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

func TestDataProbeTellsFlowFromStall(t *testing.T) {
	big := strings.Repeat("x", siteDataLimit)
	for name, tc := range map[string]struct {
		r    io.Reader
		want string
	}{
		"flows":       {strings.NewReader(big), ""},
		"small reply": {strings.NewReader("HTTP/1.1 301 Moved\r\n\r\n"), ""},
		"stall":       {io.MultiReader(strings.NewReader(strings.Repeat("x", 12<<10)), timeoutReader{}), "stall 12KB"},
		"nothing":     {timeoutReader{}, "no data"},
	} {
		err := readData(tc.r)
		got := ""
		if err != nil {
			got = shortError(err)
		}
		if got != tc.want {
			t.Errorf("%s: %q", name, got)
		}
	}
}

type timeoutReader struct{}

func (timeoutReader) Read([]byte) (int, error) { return 0, timeoutErr{} }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// A bare name without an address is tested as www.; without either, one
// "no address" cell and the rest skipped.
func TestNetworkTestTriesWWWThenSaysNoAddress(t *testing.T) {
	tt := newBrowserTunnel(t, []SiteGroup{{Via: "smart", Hosts: []string{"bare.example.org", "gone.example.org"}}}, nil, nil)
	tt.cfg.Canaries, tt.cfg.References = nil, nil
	saved := diagTelegramDCs
	diagTelegramDCs = nil
	defer func() { diagTelegramDCs = saved }()
	notFound := &net.DNSError{Err: "no such host", IsNotFound: true}
	tt.siteDNS = fakeSiteDNS(func(_ context.Context, h string) ([]string, error) {
		if h == "www.bare.example.org" {
			return []string{"192.0.2.7"}, nil
		}
		return nil, notFound
	}, func(context.Context, string) ([]string, error) { return nil, notFound })
	tt.strategies = []strategy{{"tlsfrag", echoDialer("s:", new(atomic.Int32))}}
	tt.ways[RouteDirect] = way{echoDialer("d:", new(atomic.Int32)), time.Second}
	var mu sync.Mutex
	var snis []string
	tt.siteDataProbe = func(_ context.Context, _ dialFunc, addr, sni string) error {
		mu.Lock()
		snis = append(snis, sni+"@"+addr)
		mu.Unlock()
		return nil
	}
	d := tt.Diagnose(context.Background())
	byHost := map[string]DiagRow{}
	for _, r := range d.Rows {
		byHost[r.Host] = r
	}
	www, gone := byHost["www.bare.example.org"], byHost["gone.example.org"]
	if !www.Results[0].OK || !www.Results[1].OK {
		t.Fatalf("www row %+v", www)
	}
	if gone.Results[0].Error != "no address" || !gone.Results[1].Skip {
		t.Fatalf("gone row %+v", gone)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, s := range snis {
		if s != "www.bare.example.org@192.0.2.7:443" {
			t.Fatalf("probed %v", snis)
		}
	}
}
