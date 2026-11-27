package tunnel

import (
	"context"
	"encoding/json"
	"errors"
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
		if !strings.HasPrefix(r.Target, "site-") || !r.Results[0].OK {
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
	// The label matches the one in the site log lines of this run.
	if !strings.HasPrefix(smart.Target, tt.siteDiag().mask("example.org")) {
		t.Fatalf("%q", smart.Target)
	}
}
