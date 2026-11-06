package tunnel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) }))
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
