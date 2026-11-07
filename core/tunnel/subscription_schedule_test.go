package tunnel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

func TestSubscriptionScheduleFakeClock(t *testing.T) {
	var s subscriptionSchedule
	zero := func(time.Duration) time.Duration { return 0 }
	max := func(d time.Duration) time.Duration { return d }
	if s.first(zero) != 5*time.Second || s.first(max) != time.Minute {
		t.Fatal("first jitter")
	}
	for _, want := range []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour, 6 * time.Hour} {
		if got := s.next(false, zero); got != want {
			t.Fatal(got, want)
		}
	}
	if s.next(true, max) != 12*time.Hour || s.next(false, zero) != 5*time.Minute {
		t.Fatal("success reset")
	}
	now := time.Unix(1000, 0)
	if !s.urgent(now) || s.urgent(now.Add(14*time.Minute)) || !s.urgent(now.Add(15*time.Minute)) {
		t.Fatal("urgent rate limit")
	}
}
func TestSubscriptionStatusOmitsSecrets(t *testing.T) {
	sub, _, _ := subFixture(t)
	tun, err := Start(Config{Subscriptions: []Subscription{sub}})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	raw, err := json.Marshal(tun.Status(0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "read_key") || strings.Contains(string(raw), "sources") || strings.Contains(string(raw), base64.StdEncoding.EncodeToString(sub.ReadKey)) {
		t.Fatal("secret in status")
	}
	if !strings.Contains(string(raw), `"id":"01010101"`) {
		t.Fatal("status prefix")
	}
}

func TestSubscriptionRuntimePublishesPrivateConfig(t *testing.T) {
	sub, key, bundle := subFixture(t)
	raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(raw) }))
	defer server.Close()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub.Sources = []string{server.URL}
	priority := uint16(5)
	cfg := Config{Subscriptions: []Subscription{sub}, Routes: []links.Route{subRoute("manual")}, RouteEdits: map[string]RouteEdit{"manual": {Off: true, Priority: &priority}}}
	tun, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	out := tun.RefreshSubscriptions(context.Background())
	if !out.Changed || len(out.Config.Routes) != 2 || !out.Config.RouteEdits["manual"].Off {
		t.Fatal("private config lost installed routes")
	}
	before := requests.Load()
	pending := tun.RefreshSubscriptions(context.Background())
	if !pending.Changed || len(pending.Config.Routes) != 2 || requests.Load() != before {
		t.Fatal("pending private config fetched again")
	}
	if !tun.Status(0).ConfigChanged || tun.Status(0).Subscriptions[0].Revision != 1 {
		t.Fatal("missing status notification")
	}
	select {
	case update := <-tun.SubscriptionUpdates():
		if !update.Changed || len(update.Config.Routes) != 2 {
			t.Fatal("missing config")
		}
	default:
		t.Fatal("no update")
	}
}

func TestClosedSubscriptionRuntime(t *testing.T) {
	sub, key, bundle := subFixture(t)
	sub.RouteIDs, sub.Revision = []string{"one"}, 1
	raw, err := links.SealClosedSubFile(bundle.Sub, sub.ReadKey, 2, key)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(raw) }))
	defer server.Close()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub.Sources = []string{server.URL}
	cfg := Config{Subscriptions: []Subscription{sub}, Routes: []links.Route{subRoute("one")}, RouteEdits: map[string]RouteEdit{"one": {Off: true}}}
	tun, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := tun.RefreshSubscriptions(context.Background())
	status := tun.Status(0)
	tun.Close()
	if !out.Changed || !status.ConfigChanged || !status.Subscriptions[0].Closed || len(out.Config.Routes) != 0 || len(out.Config.RouteEdits) != 0 || out.Config.Subscriptions[0].LastTry == 0 {
		t.Fatal("closed runtime", out, status)
	}
	path := t.TempDir() + "/subscriptions.json"
	if err := SaveSubscriptionState(path, out.Config); err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSubscriptionState(cfg, path)
	if err != nil || !restored.Subscriptions[0].Closed || len(restored.Routes) != 0 || len(restored.RouteEdits) != 0 {
		t.Fatal("closed persistence", restored, err)
	}
	restarted, err := Start(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	again := restarted.RefreshSubscriptions(context.Background())
	if again.Changed || requests.Load() != 1 {
		t.Fatal("closed restart fetched")
	}
	// An all-closed schedule exits without waiting for its first timer.
	finished := make(chan struct{})
	restarted.wg.Add(1)
	go func() { restarted.subscriptionLoop(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("closed schedule is active")
	}
}
