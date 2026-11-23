package tunnel

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	links "github.com/mrgluek/passage-link/link"
	"golang.org/x/crypto/chacha20poly1305"
)

func TestSubscriptionSourceFetchStats(t *testing.T) {
	sub, key, bundle := subFixture(t)
	raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := chacha20poly1305.NewX(sub.ReadKey)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := aead.Open(nil, raw[5:29], raw[29:], append([]byte("PSUB1"), bundle.Sub...))
	if err != nil {
		t.Fatal(err)
	}
	plain[4+int(binary.BigEndian.Uint32(plain))-1] ^= 1
	badSignature := aead.Seal(append([]byte(nil), raw[:29]...), raw[5:29], plain, append([]byte("PSUB1"), bundle.Sub...))
	var mode atomic.Value
	mode.Store("ok")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load().(string) {
		case "http":
			w.WriteHeader(http.StatusNotFound)
		case "bad":
			w.Write(badSignature)
		case "304":
			w.WriteHeader(http.StatusNotModified)
		default:
			time.Sleep(3 * time.Millisecond)
			w.Header().Set("ETag", `"one"`)
			w.Write(raw)
		}
	}))
	defer server.Close()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub.Sources = []string{server.URL}
	cfg := Config{Subscriptions: []Subscription{sub}}
	ref := SubscriptionRefresher{}
	dial := (&net.Dialer{}).DialContext
	cfg = ref.Refresh(context.Background(), cfg, nil).Config
	first := cfg.Subscriptions[0].SourceStats[sourceStatsKey(server.URL)]
	if first.Tries != 1 || first.Answered != 1 || first.OK != 1 || first.Via.Direct != 1 || first.LastOK == 0 || first.LastMS < 3 {
		t.Fatalf("direct: %+v", first)
	}
	mode.Store("304")
	cfg = ref.Refresh(context.Background(), cfg, []SubscriptionDial{dial}).Config
	mode.Store("ok")
	cfg = ref.Refresh(context.Background(), cfg, []SubscriptionDial{dial}).Config
	ref.directTimeout = 10 * time.Millisecond
	ref.directDial = func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	cfg = ref.Refresh(context.Background(), cfg, nil).Config
	ref.directDial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unavailable") }
	cfg.Operator = "other"
	cfg = ref.Refresh(context.Background(), cfg, nil).Config
	ref.directDial = dial
	cfg.Operator = "third"
	ref.directTimeout = time.Second
	mode.Store("http")
	cfg = ref.Refresh(context.Background(), cfg, nil).Config
	mode.Store("bad")
	cfg = ref.Refresh(context.Background(), cfg, nil).Config
	if cfg.Subscriptions[0].LastError != "signature" {
		t.Fatal("expected signature rejection", cfg.Subscriptions[0].LastError)
	}
	got := cfg.Subscriptions[0].SourceStats[sourceStatsKey(server.URL)]
	if got.Tries != 9 || got.Answered != 7 || got.OK != 5 || got.Fail.Net != 1 || got.Fail.Timeout != 1 || got.Fail.HTTP != 1 || got.Fail.Bad != 1 || got.Via.Direct != 3 || got.Via.Tunnel != 2 || got.LastOK == 0 || got.LastMS < 3 {
		t.Fatalf("sequence: %+v", got)
	}
	if len(sub.SourceStats) != 0 {
		t.Fatal("mutated original subscription")
	}
}

func TestSubscriptionSourceStatsSaturate(t *testing.T) {
	sub, key, bundle := subFixture(t)
	raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var mode atomic.Value
	mode.Store("ok")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load().(string) {
		case "http":
			w.WriteHeader(404)
		case "429":
			w.WriteHeader(http.StatusTooManyRequests)
		case "503":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "bad":
			w.Write([]byte("invalid"))
		default:
			w.Write(raw)
		}
	}))
	defer server.Close()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub.Sources = []string{server.URL}
	full := SubscriptionSourceStats{Tries: math.MaxUint32, Answered: math.MaxUint32, OK: math.MaxUint32,
		Fail:   SubscriptionSourceFailures{Net: math.MaxUint32, Timeout: math.MaxUint32, HTTP: math.MaxUint32, Bad: math.MaxUint32},
		Via:    SubscriptionSourceVia{Direct: math.MaxUint32, Tunnel: math.MaxUint32},
		Direct: SubscriptionSourceAttempts{Tries: math.MaxUint32, Answered: math.MaxUint32, OK: math.MaxUint32, HTTP429: math.MaxUint32, HTTP5xx: math.MaxUint32, Fail: SubscriptionSourceFailures{Net: math.MaxUint32, Timeout: math.MaxUint32, HTTP: math.MaxUint32, Bad: math.MaxUint32}},
		Tunnel: SubscriptionSourceAttempts{Tries: math.MaxUint32, Answered: math.MaxUint32, OK: math.MaxUint32, HTTP429: math.MaxUint32, HTTP5xx: math.MaxUint32, Fail: SubscriptionSourceFailures{Net: math.MaxUint32, Timeout: math.MaxUint32, HTTP: math.MaxUint32, Bad: math.MaxUint32}}}
	sub.SourceStats = map[string]SubscriptionSourceStats{sourceStatsKey(server.URL): full}
	cfg := Config{Subscriptions: []Subscription{sub}}
	ref := SubscriptionRefresher{directTimeout: time.Second}
	cfg = ref.Refresh(context.Background(), cfg, nil).Config
	// A failed direct attempt marks the source, so the next refresh goes
	// straight to the tunnel; each answer below must really use it.
	var tunnelCalls atomic.Int32
	tunnel := func(ctx context.Context, network, address string) (net.Conn, error) {
		tunnelCalls.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	ref.directDial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unavailable") }
	cfg.Operator = "tunnel"
	cfg = ref.Refresh(context.Background(), cfg, nil).Config
	for _, m := range []string{"ok", "429", "503"} {
		mode.Store(m)
		cfg = ref.Refresh(context.Background(), cfg, []SubscriptionDial{tunnel}).Config
	}
	if tunnelCalls.Load() != 3 {
		t.Fatalf("tunnel dials: %d, want 3", tunnelCalls.Load())
	}
	ref.directDial = nil
	for _, m := range []string{"http", "bad", "429", "503", "net", "timeout"} {
		mode.Store(m)
		cfg.Operator = m
		switch m {
		case "net":
			ref.directDial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unavailable") }
		case "timeout":
			ref.directTimeout = 10 * time.Millisecond
			ref.directDial = func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
		default:
			ref.directDial = nil
		}
		cfg = ref.Refresh(context.Background(), cfg, nil).Config
	}
	got := cfg.Subscriptions[0].SourceStats[sourceStatsKey(server.URL)]
	got.LastOK, got.LastMS = 0, 0
	if got != full {
		t.Fatalf("wrapped: %+v", got)
	}
	if sub.SourceStats[sourceStatsKey(server.URL)] != full {
		t.Fatal("mutated input counters")
	}
}

func TestSubscriptionSourceStatsPersistenceAndPruning(t *testing.T) {
	sub, _, _ := subFixture(t)
	one, two, three := "https://example.org/one", "https://example.org/two", "https://example.org/three"
	sub.Sources = []string{one, two, three}
	sub.SourceStats = map[string]SubscriptionSourceStats{sourceStatsKey(one): {Tries: 1, Answered: 2, OK: 3, Fail: SubscriptionSourceFailures{Net: 4, Timeout: 5, HTTP: 6, Bad: 7}, Via: SubscriptionSourceVia{Direct: 8, Tunnel: 9}, Direct: SubscriptionSourceAttempts{Tries: 10}, Tunnel: SubscriptionSourceAttempts{Tries: 11}, LastOK: 100, LastMS: 20}, sourceStatsKey(two): {Tries: 2}, sourceStatsKey(three): {Tries: 3}}
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	if err := SaveSubscriptionState(path, Config{Subscriptions: []Subscription{sub}}); err != nil {
		t.Fatal(err)
	}
	fresh := sub
	fresh.SourceStats = nil
	fresh.Sources = []string{three, one + "/"}
	restored, err := RestoreSubscriptionState(Config{Subscriptions: []Subscription{fresh}}, path)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.Subscriptions[0].SourceStats
	if len(got) != 2 || got[sourceStatsKey(one)].Tries != 1 || got[sourceStatsKey(three)].Tries != 3 {
		t.Fatalf("restore: %+v", got)
	}
	normalized, err := (Config{Subscriptions: []Subscription{restored.Subscriptions[0]}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized.Subscriptions[0].SourceStats, got) {
		t.Fatal("normalize changed counters")
	}
	if err := SaveSubscriptionState(path, restored); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sourceStatsKey(two)) {
		t.Fatal("removed source persisted")
	}
	if got[sourceStatsKey(one)] != sub.SourceStats[sourceStatsKey(one)] {
		t.Fatal("lost saved fields")
	}
	if len(sub.SourceStats) != 3 {
		t.Fatal("pruning mutated caller")
	}
}

func TestSubscriptionSourceStatusLabelsAndPrivacy(t *testing.T) {
	sub, _, _ := subFixture(t)
	sub.InviteSources = []string{"https://example.org/shared/", "https://example.org/invite"}
	sub.BundleSources = []string{"https://example.org/bundle", "https://example.org/shared"}
	sub.Sources = mergeSubscriptionSources(sub.BundleSources, sub.InviteSources)
	sub.SourceStats = map[string]SubscriptionSourceStats{}
	for _, source := range sub.Sources {
		sub.SourceStats[sourceStatsKey(source)] = SubscriptionSourceStats{Tries: 1}
	}
	rows := subscriptionStatuses(Config{Subscriptions: []Subscription{sub}})
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"bundle-1", "bundle-2", "invite-2"}
	if len(rows[0].Sources) != len(want) {
		t.Fatal("source count", rows)
	}
	for i, source := range rows[0].Sources {
		if source.ID != want[i] {
			t.Fatalf("label %d: %s", i, source.ID)
		}
	}
	for _, private := range []string{"https", "example.org", "/shared", "source_stats", `"fail"`, `"via"`, `"last_ms"`} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("unexpected %q in %s", private, raw)
		}
	}
	sub.InviteSources, sub.BundleSources = nil, nil
	rows = subscriptionStatuses(Config{Subscriptions: []Subscription{sub}})
	if rows[0].Sources[0].ID != "invite-1" {
		t.Fatal("legacy source label", rows)
	}
}

func TestSubscriptionSourceStatusBeforeFetchUnchanged(t *testing.T) {
	sub, _, _ := subFixture(t)
	raw, err := json.Marshal(subscriptionStatuses(Config{Subscriptions: []Subscription{sub}})[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"01010101","name":"Example","revision":0,"routes":0,"last_ok":0,"last_try":0}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestMergeSubscriptionSourcesTrailingSlashes(t *testing.T) {
	bundle := []string{"HTTPS://Example.org/shared/", "https://example.org/bundle"}
	invite := []string{"https://example.org/shared", "https://example.org/invite/", "https://example.org/invite", "https://example.org/SHARED"}
	want := []string{bundle[0], bundle[1], invite[1], invite[3]}
	if got := mergeSubscriptionSources(bundle, invite); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	var many []string
	for _, path := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		many = append(many, "https://example.org/"+path)
	}
	if got := mergeSubscriptionSources(many[:4], many[4:]); len(got) != 8 || !reflect.DeepEqual(got, many[:8]) {
		t.Fatal("cap/order", got)
	}
}

func TestSubscriptionSourceStatsReorder(t *testing.T) {
	sub, _, _ := subFixture(t)
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	var ref SubscriptionRefresher
	failures := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer failures.Close()
	one, two := failures.URL+"/one", failures.URL+"/two"
	sub.Sources = []string{one, two}
	cfg := ref.Refresh(context.Background(), Config{Subscriptions: []Subscription{sub}}, nil).Config
	cfg.Subscriptions[0].Sources = []string{two, one}
	cfg = ref.Refresh(context.Background(), cfg, nil).Config
	for _, source := range []string{one, two} {
		stats := cfg.Subscriptions[0].SourceStats[sourceStatsKey(source)]
		if stats.Tries != 2 || stats.Answered != 2 || stats.Fail.HTTP != 2 {
			t.Fatalf("reordered: %+v", stats)
		}
	}
	cfg.Subscriptions[0].Sources = []string{two}
	cfg = ref.Refresh(context.Background(), cfg, nil).Config
	stats := cfg.Subscriptions[0].SourceStats[sourceStatsKey(two)]
	if len(cfg.Subscriptions[0].SourceStats) != 1 || stats.Tries != 3 || stats.Fail.HTTP != 3 {
		t.Fatal("removed source not pruned", cfg.Subscriptions[0].SourceStats)
	}
}

func TestSubscriptionSourceStatsPartialAnswers(t *testing.T) {
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "malformed response", true: "body timeout"}[timeout], func(t *testing.T) {
			sub, _, _ := subFixture(t)
			sub.Sources = []string{"http://example.org/sub"}
			ref := SubscriptionRefresher{directTimeout: 50 * time.Millisecond}
			// The server waits for the test to end, so the only failure the client
			// can see is its own deadline, not a closed connection.
			release := make(chan struct{})
			defer close(release)
			ref.directDial = func(ctx context.Context, _, _ string) (net.Conn, error) {
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					request := make([]byte, 4096)
					if _, err := server.Read(request); err != nil {
						return
					}
					if timeout {
						server.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\npartial"))
						<-release
					} else {
						server.Write([]byte("invalid HTTP\r\n\r\n"))
					}
				}()
				return client, nil
			}
			cfg := ref.Refresh(context.Background(), Config{Subscriptions: []Subscription{sub}}, nil).Config
			stats := cfg.Subscriptions[0].SourceStats[sourceStatsKey(sub.Sources[0])]
			if stats.Tries != 1 || stats.Answered != 1 || stats.OK != 0 {
				t.Fatalf("partial: %+v", stats)
			}
			if timeout && stats.Fail.Timeout != 1 || !timeout && stats.Fail.Net != 1 {
				t.Fatalf("classification: %+v", stats)
			}
		})
	}
}

func TestRouterEventMasksLocalEndpoint(t *testing.T) {
	log := newEventLog(1)
	log.add("read udp [2001:db8::8%%en0]:40112->[2001:db8:1::53]:53: i/o timeout")
	got := log.last(1)[0].Msg
	if got != "read udp [addr]->[2001:db8:1::53]:53: i/o timeout" {
		t.Fatalf("event: %q", got)
	}
}

func TestSourceAttemptCounters(t *testing.T) {
	for _, tunnel := range []bool{false, true} {
		for _, failure := range []string{"", "net", "timeout", "http", "bad", "canceled"} {
			t.Run(fmt.Sprintf("%t/%s", tunnel, failure), func(t *testing.T) {
				sub := Subscription{}
				sub.recordSourceFetch("https://example.org", subscriptionFetchStats{started: true, answered: true, tunnel: tunnel, failure: failure})
				stats := sub.SourceStats[sourceStatsKey("https://example.org")]
				split, other := stats.Direct, stats.Tunnel
				if tunnel {
					split, other = other, split
				}
				want := SubscriptionSourceFailures{}
				switch failure {
				case "net":
					want.Net = 1
				case "timeout":
					want.Timeout = 1
				case "http":
					want.HTTP = 1
				case "bad":
					want.Bad = 1
				}
				if split.Tries != 1 || split.Answered != 1 || split.Fail != want || stats.Fail != want || other != (SubscriptionSourceAttempts{}) {
					t.Fatalf("counters: %+v", stats)
				}
			})
		}
	}
}
