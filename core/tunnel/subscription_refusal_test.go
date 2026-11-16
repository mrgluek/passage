package tunnel

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

type refusalFixture struct {
	sub                       Subscription
	hostA, hostB              string
	tunnelA, tunnelB          atomic.Int32
	directA, directB          atomic.Int32
	refuseB                   atomic.Bool
	bRefused                  chan struct{}
	tunnelDial, directDial    SubscriptionDial
	ref                       *SubscriptionRefresher
	keyB                      string
	cfg                       Config
	serverA, serverB          *httptest.Server
	refreshWithTunnelFromHere func() SubscriptionRefreshResult
}

func newRefusalFixture(t *testing.T) *refusalFixture {
	t.Helper()
	sub, key, bundle := subFixture(t)
	raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) })
	f := &refusalFixture{bRefused: make(chan struct{})}
	f.serverA, f.serverB = httptest.NewServer(handler), httptest.NewServer(handler)
	t.Cleanup(f.serverA.Close)
	t.Cleanup(f.serverB.Close)
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub.Sources = []string{f.serverA.URL, f.serverB.URL}
	for _, u := range []struct {
		raw  string
		host *string
	}{{f.serverA.URL, &f.hostA}, {f.serverB.URL, &f.hostB}} {
		parsed, _ := url.Parse(u.raw)
		*u.host = parsed.Host
	}
	f.sub, f.keyB = sub, sourceStatsKey(f.serverB.URL)
	real := (&net.Dialer{}).DialContext
	var once atomic.Bool
	f.tunnelDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == f.hostB {
			f.tunnelB.Add(1)
			if f.refuseB.Load() {
				if once.CompareAndSwap(false, true) {
					close(f.bRefused)
				}
				return nil, errDestinationRefused
			}
			return real(ctx, network, address)
		}
		f.tunnelA.Add(1)
		if f.refuseB.Load() {
			select {
			case <-f.bRefused:
				time.Sleep(60 * time.Millisecond)
			case <-time.After(time.Second):
			case <-ctx.Done():
			}
		}
		return real(ctx, network, address)
	}
	f.ref = &SubscriptionRefresher{
		tunnelTimeout: 2 * time.Second,
		directTimeout: 2 * time.Second,
		directDial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == f.hostB {
				f.directB.Add(1)
			} else {
				f.directA.Add(1)
			}
			return real(ctx, network, address)
		},
	}
	f.cfg = Config{Subscriptions: []Subscription{sub}}
	return f
}

func (f *refusalFixture) refresh(t *testing.T) SubscriptionRefreshResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := f.ref.Refresh(ctx, f.cfg, []SubscriptionDial{f.tunnelDial})
	f.cfg = out.Config
	return out
}

func TestTunnelRefusalIsCountedAndRemembered(t *testing.T) {
	f := newRefusalFixture(t)
	f.refuseB.Store(true)
	out := f.refresh(t)
	stats := out.Config.Subscriptions[0].SourceStats[f.keyB]
	if stats.Tunnel.Fail.Refused != 1 || stats.Tunnel.Fail.Net != 0 || stats.Fail.Refused != 1 || stats.TunnelRefusedAt == 0 {
		t.Fatalf("refusal not recorded as such: %+v", stats)
	}
	if a := out.Config.Subscriptions[0].SourceStats[sourceStatsKey(f.serverA.URL)]; a.TunnelRefusedAt != 0 || a.Tunnel.OK != 1 {
		t.Fatalf("the other source was marked: %+v", a)
	}
}

func TestRefusedSourceIsFetchedDirectlyNextTime(t *testing.T) {
	f := newRefusalFixture(t)
	f.refuseB.Store(true)
	f.refresh(t)
	tunnelB, directB := f.tunnelB.Load(), f.directB.Load()
	out := f.refresh(t)
	if f.tunnelB.Load() != tunnelB {
		t.Fatal("the tunnel was asked again for a source it had refused")
	}
	if f.directB.Load() != directB+1 {
		t.Fatalf("direct fetches of the refused source: %d, want %d", f.directB.Load(), directB+1)
	}
	stats := out.Config.Subscriptions[0].SourceStats[f.keyB]
	if stats.Direct.OK < 1 || stats.Tunnel.Fail.Refused != 1 {
		t.Fatalf("%+v", stats)
	}
	if f.directA.Load() != 0 {
		t.Fatal("a source the tunnel carries was fetched directly")
	}
}

func TestRefusalMemoryExpiresAndSuccessClearsIt(t *testing.T) {
	f := newRefusalFixture(t)
	f.refuseB.Store(true)
	f.refresh(t)
	// An old refusal is forgotten, and the exits may have been updated since.
	f.refuseB.Store(false)
	sub := f.cfg.Subscriptions[0]
	stats := sub.SourceStats[f.keyB]
	stats.TunnelRefusedAt = time.Now().Add(-tunnelRefusalMemory - time.Hour).Unix()
	sub.SourceStats[f.keyB] = stats
	f.cfg.Subscriptions[0] = sub
	tunnelB := f.tunnelB.Load()
	out := f.refresh(t)
	if f.tunnelB.Load() != tunnelB+1 {
		t.Fatal("the tunnel was not tried again after the memory expired")
	}
	if got := out.Config.Subscriptions[0].SourceStats[f.keyB]; got.TunnelRefusedAt != 0 || got.Tunnel.OK < 1 {
		t.Fatalf("a tunnel success must clear the refusal: %+v", got)
	}
}

func TestRouteRefusedSourcesDirectSwapsOnlyRecentRefusals(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	sub := Subscription{SourceStats: map[string]SubscriptionSourceStats{
		sourceStatsKey("https://a.example.org"): {TunnelRefusedAt: now.Add(-time.Hour).Unix()},
		sourceStatsKey("https://b.example.org"): {TunnelRefusedAt: now.Add(-tunnelRefusalMemory - time.Second).Unix()},
	}}
	pairs := []subscriptionPair{{"https://a.example.org", 0}, {"https://a.example.org", 1}, {"https://b.example.org", 0}, {"https://b.example.org", 1}}
	moved := map[string]bool{}
	got := routeRefusedSourcesDirect(sub, pairs, 2, now, moved)
	want := []subscriptionPair{{"https://a.example.org", 2}, {"https://b.example.org", 0}, {"https://b.example.org", 1}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v, want %v", got, want)
		}
	}
	if !moved["https://a.example.org"] || moved["https://b.example.org"] {
		t.Fatal(moved)
	}
}
