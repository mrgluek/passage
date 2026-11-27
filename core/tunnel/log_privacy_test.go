package tunnel

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveEventsRedactSecrets(t *testing.T) {
	log := newEventLog(4)
	log.add("request failed: token=%s", "private-value")
	log.add("request failed: %s", "https://example.org/private")
	for _, event := range log.last(4) {
		if strings.Contains(event.Msg, "private-value") || strings.Contains(event.Msg, "example.org") {
			t.Fatal(event.Msg)
		}
	}
}

func siteLines(tt *Tunnel) []string {
	var out []string
	for _, e := range tt.log.last(100) {
		if strings.Contains(e.Msg, "site-") || strings.Contains(e.Msg, "browser port") {
			out = append(out, e.Msg)
		}
	}
	return out
}

func failingSite(t *testing.T, err error) (*Tunnel, *atomic.Int64) {
	t.Helper()
	var now atomic.Int64
	now.Store(1_000_000)
	fail := func(context.Context, string, string) (net.Conn, error) { return nil, err }
	tt := newBrowserTunnel(t, []SiteGroup{{Via: "tunnel", Hosts: []string{"private-site.example.org"}}}, fail, fail)
	tt.now = func() time.Time { return time.Unix(now.Load(), 0) }
	return tt, &now
}

func TestFailedSiteLineIsMaskedAndHasTheFacts(t *testing.T) {
	tt, _ := failingSite(t, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connection refused to private-site.example.org")})
	if _, rep := browserSocks(t, tt, "private-site.example.org", nil); rep != repHostUnreachable {
		t.Fatal(rep)
	}
	var fail string
	for _, l := range siteLines(tt) {
		if strings.Contains(l, "private-site") || strings.Contains(l, "example.org") {
			t.Fatalf("site name in log: %s", l)
		}
		if strings.Contains(l, "failed:") {
			fail = l
		}
	}
	for _, want := range []string{"site-", "(tunnel)", "connect", "tried direct,tunnel", " ms"} {
		if !strings.Contains(fail, want) {
			t.Errorf("line lacks %q: %s", want, fail)
		}
	}
	// The same host gets the same label; the SOCKS error is logged without a name.
	if !strings.Contains(strings.Join(siteLines(tt), "\n"), "SOCKS error reply 0x04") {
		t.Fatal("SOCKS error reply not logged")
	}
}

func TestSiteFailureLinesAreRateLimitedPerHost(t *testing.T) {
	tt, now := failingSite(t, errDestinationRefused)
	for i := 0; i < 5; i++ {
		browserSocks(t, tt, "private-site.example.org", nil)
	}
	count := func() (n int) {
		for _, l := range siteLines(tt) {
			if strings.Contains(l, "failed:") {
				n++
			}
		}
		return
	}
	if count() != 1 {
		t.Fatalf("%d lines for a retry storm: %v", count(), siteLines(tt))
	}
	now.Add(61)
	browserSocks(t, tt, "private-site.example.org", nil)
	lines := siteLines(tt)
	if count() != 2 || !strings.Contains(strings.Join(lines, "\n"), "(+4 suppressed)") || !strings.Contains(strings.Join(lines, "\n"), "refused by exit") {
		t.Fatalf("%v", lines)
	}
	// Another host has its own allowance.
	other := tt.siteDiag()
	other.mu.Lock()
	ok, _ := other.allow("another-host", time.Unix(now.Load(), 0))
	other.mu.Unlock()
	if !ok {
		t.Fatal("hosts share one allowance")
	}
}

func TestSiteSummaryLineHasNoNames(t *testing.T) {
	var calls atomic.Int32
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", &calls), echoDialer("t:", &calls))
	for i := 0; i < 3; i++ {
		c, _ := browserSocks(t, tt, "www.example.org", nil)
		roundTrip(t, c)
	}
	browserSocks(t, tt, "unlisted.example.com", nil) // refused: a SOCKS error
	tt.flushSiteSummary(true)
	var summary string
	for _, l := range siteLines(tt) {
		if strings.Contains(l, "example") {
			t.Fatalf("name in log: %s", l)
		}
		if strings.Contains(l, "connections ok") {
			summary = l
		}
	}
	if !strings.Contains(summary, "3 connections ok, 0 failed, 1 SOCKS errors") {
		t.Fatalf("summary %q in %v", summary, siteLines(tt))
	}
	n := len(siteLines(tt))
	tt.flushSiteSummary(true) // no traffic since: no line
	if len(siteLines(tt)) != n {
		t.Fatal("an empty summary was written")
	}
}
