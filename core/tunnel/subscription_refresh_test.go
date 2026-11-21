package tunnel

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

type subscriptionTestReply func(context.Context, *http.Request) *http.Response

func subscriptionTestDial(reply subscriptionTestReply) SubscriptionDial {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			if res := reply(ctx, req); res != nil {
				res.Write(server)
			}
		}()
		return client, nil
	}
}

func subscriptionTestResponse(status int, body string) *http.Response {
	res := &http.Response{
		StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header),
		Body: http.NoBody,
	}
	if body != "" {
		res.Body = io.NopCloser(strings.NewReader(body))
		res.ContentLength = int64(len(body))
	}
	return res
}

// subscriptionTestConfigNewer is subscriptionTestConfig plus a sealed file of the next revision.
func subscriptionTestConfigNewer(t *testing.T, sources ...string) (Config, []byte, []byte) {
	t.Helper()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub, key, bundle := subFixture(t)
	raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Revision++
	newer, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sub.Sources = sources
	return Config{Subscriptions: []Subscription{sub}}, raw, newer
}

func subscriptionTestConfig(t *testing.T, sources ...string) (Config, []byte) {
	t.Helper()
	subscriptionTestHTTP = true
	t.Cleanup(func() { subscriptionTestHTTP = false })
	sub, key, bundle := subFixture(t)
	raw, err := links.SealSubFile(bundle, bundle.Sub, sub.ReadKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sub.Sources = sources
	return Config{Subscriptions: []Subscription{sub}}, raw
}

func TestSubscriptionDirectStagger(t *testing.T) {
	for _, test := range []struct {
		name      string
		unchanged bool
		newer     bool
		remember  bool
	}{
		{name: "second wins"},
		{name: "unchanged waits for the others", unchanged: true},
		{name: "newer file after unchanged", unchanged: true, newer: true},
		{name: "last winner first", remember: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, raw, newer := subscriptionTestConfigNewer(t, "http://one.example.org", "http://two.example.org", "http://three.example.org")
			sub := &cfg.Subscriptions[0]
			if test.unchanged {
				// Only the first source gets the saved tag; the others answer with a file.
				sub.Revision, sub.ETag, sub.ETagSource = 1, "saved", sub.Sources[0]
			}
			synctest.Test(t, func(t *testing.T) {
				first := "one.example.org"
				var ref SubscriptionRefresher
				if test.remember {
					first = "two.example.org"
					ref.last = map[string]subscriptionPair{networkKey(cfg) + ":" + sub.ID: {sub.Sources[1], 0}}
				}
				start := time.Now()
				loserCanceled := make(chan struct{})
				ref.directDial = func(ctx context.Context, network, address string) (net.Conn, error) {
					if address == first+":80" {
						if time.Since(start) != 0 {
							t.Errorf("first direct dial at %v", time.Since(start))
						}
						if !test.unchanged && !test.remember {
							<-ctx.Done()
							close(loserCanceled)
							return nil, ctx.Err()
						}
					} else if test.unchanged {
						want := map[string]time.Duration{"two.example.org:80": 2 * time.Second, "three.example.org:80": 4 * time.Second}
						if at, ok := want[address]; !ok || time.Since(start) != at || test.newer && address != "two.example.org:80" {
							t.Errorf("unexpected direct dial %s at %v", address, time.Since(start))
						}
					} else if test.remember || address != "two.example.org:80" || time.Since(start) != 2*time.Second {
						t.Errorf("unexpected direct dial %s at %v", address, time.Since(start))
					}
					dial := subscriptionTestDial(func(_ context.Context, req *http.Request) *http.Response {
						if test.unchanged && strings.HasPrefix(req.Host, "one.example.org") {
							if req.Header.Get("If-None-Match") != "saved" {
								t.Error("missing conditional request")
							}
							return subscriptionTestResponse(http.StatusNotModified, "")
						}
						if test.newer {
							return subscriptionTestResponse(http.StatusOK, string(newer))
						}
						return subscriptionTestResponse(http.StatusOK, string(raw))
					})
					return dial(ctx, network, address)
				}
				tunnel := func(context.Context, string, string) (net.Conn, error) {
					t.Error("unexpected tunnel dial")
					return nil, errors.New("unexpected tunnel")
				}
				out := ref.Refresh(context.Background(), cfg, []SubscriptionDial{tunnel})
				got := out.Config.Subscriptions[0]
				if test.unchanged && !test.newer {
					if out.Changed || got.LastError != "unchanged" || time.Since(start) != 4*time.Second {
						t.Fatalf("unchanged did not wait for the other sources: %q at %v", got.LastError, time.Since(start))
					}
				} else if test.newer {
					first := got.SourceStats[sourceStatsKey(sub.Sources[0])]
					if !out.Changed || got.ETagSource != sub.Sources[1] || time.Since(start) != 2*time.Second || first.Fail.HTTP != 0 || first.OK != 1 {
						t.Fatalf("a newer file did not beat unchanged: changed %v at %v, first source %+v", out.Changed, time.Since(start), first)
					}
				} else if !out.Changed || got.ETagSource != sub.Sources[1] {
					t.Fatal("second source did not win")
				}
				if !test.unchanged && !test.remember {
					synctest.Wait()
					select {
					case <-loserCanceled:
					default:
						t.Fatal("losing direct dial continued")
					}
				}
			})
		})
	}
}

func TestSubscriptionTunnelDelay(t *testing.T) {
	for _, withTunnel := range []bool{false, true} {
		t.Run(strconv.FormatBool(withTunnel), func(t *testing.T) {
			cfg, raw := subscriptionTestConfig(t, "http://example.org", "http://second.example.org")
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				ref := SubscriptionRefresher{directDial: subscriptionTestDial(func(ctx context.Context, req *http.Request) *http.Response {
					if req.Host == "second.example.org" {
						if time.Since(start) != 2*time.Second {
							t.Errorf("second direct dial at %v", time.Since(start))
						}
						return subscriptionTestResponse(http.StatusNotFound, "")
					}
					time.Sleep(21 * time.Second)
					if ctx.Err() != nil {
						t.Error("the direct attempt was canceled at the tunnel deadline")
					}
					return subscriptionTestResponse(http.StatusOK, string(raw))
				})}
				var dialers []SubscriptionDial
				tunnelCalls := make(chan struct{}, 4)
				if withTunnel {
					dialers = []SubscriptionDial{
						func(ctx context.Context, _, _ string) (net.Conn, error) {
							if time.Since(start) != 20*time.Second {
								t.Errorf("tunnel dial at %v", time.Since(start))
							}
							tunnelCalls <- struct{}{}
							<-ctx.Done()
							return nil, ctx.Err()
						},
						func(context.Context, string, string) (net.Conn, error) {
							if time.Since(start) != 20*time.Second {
								t.Errorf("tunnel dial at %v", time.Since(start))
							}
							tunnelCalls <- struct{}{}
							return nil, errDestinationRefused
						},
					}
				}
				out := ref.Refresh(context.Background(), cfg, dialers)
				if !out.Changed || time.Since(start) != 21*time.Second {
					t.Fatalf("direct did not win at 21s: %v, %v", out.Changed, time.Since(start))
				}
				synctest.Wait()
				stats := out.Config.Subscriptions[0].SourceStats[sourceStatsKey(cfg.Subscriptions[0].Sources[0])]
				if withTunnel && (len(tunnelCalls) != 4 || stats.Tunnel.Tries != 2 || stats.Tunnel.Fail.Refused != 1 || stats.Tunnel.Fail.Net != 0) {
					t.Fatalf("tunnel stats: %+v", stats.Tunnel)
				}
				if !withTunnel && stats.Tunnel.Tries != 0 {
					t.Fatalf("tunnel stats: %+v", stats.Tunnel)
				}
			})
		})
	}
}

func TestSubscriptionDirectFailureMemory(t *testing.T) {
	cfg, raw := subscriptionTestConfig(t, "http://example.org")
	other := cfg
	other.Operator = "other"
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ref := SubscriptionRefresher{}
		directDials, tunnelDials := make(chan time.Duration, 4), make(chan time.Duration, 4)
		dials := 0
		ok := subscriptionTestDial(func(context.Context, *http.Request) *http.Response {
			return subscriptionTestResponse(http.StatusOK, string(raw))
		})
		ref.directDial = func(ctx context.Context, network, address string) (net.Conn, error) {
			directDials <- time.Since(start)
			if dials++; dials == 1 {
				return nil, errors.New("connect failed")
			}
			return ok(ctx, network, address)
		}
		tunnel := func(ctx context.Context, network, address string) (net.Conn, error) {
			tunnelDials <- time.Since(start)
			return ok(ctx, network, address)
		}
		refresh := func(config Config, wantElapsed time.Duration, wantDirect, wantTunnel bool) {
			t.Helper()
			before := time.Now()
			out := ref.Refresh(context.Background(), config, []SubscriptionDial{tunnel})
			synctest.Wait()
			if !out.Changed || time.Since(before) != wantElapsed || (len(directDials) > 0) != wantDirect || (len(tunnelDials) > 0) != wantTunnel {
				t.Fatalf("refresh at %v: duration %v, changed %v, direct %d, tunnel %d", before.Sub(start), time.Since(before), out.Changed, len(directDials), len(tunnelDials))
			}
			if wantDirect {
				if at := <-directDials; at != before.Sub(start) {
					t.Fatalf("direct started at %v", at)
				}
			}
			if wantTunnel {
				if at := <-tunnelDials; at != before.Sub(start)+wantElapsed {
					t.Fatalf("tunnel started at %v", at)
				}
			}
		}
		refresh(cfg, 0, true, true)
		refresh(cfg, 0, false, true)
		refresh(other, 0, true, false)
		time.Sleep(30*time.Minute - time.Second)
		refresh(cfg, 0, false, true)
		time.Sleep(time.Second)
		refresh(cfg, 0, true, false)
	})
}

func TestSubscriptionBusySources(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		header func(time.Time) string
		wait   time.Duration
	}{
		{name: "429", status: http.StatusTooManyRequests},
		{name: "503", status: http.StatusServiceUnavailable},
		{name: "seconds", status: http.StatusTooManyRequests, header: func(time.Time) string { return "40" }, wait: 40 * time.Second},
		{name: "date", status: http.StatusServiceUnavailable, header: func(now time.Time) string { return now.Add(time.Minute).UTC().Format(http.TimeFormat) }, wait: time.Minute},
		{name: "cap", status: http.StatusServiceUnavailable, header: func(time.Time) string { return "999999999999999999999999" }, wait: time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, raw := subscriptionTestConfig(t, "http://busy.example.org", "http://fallback.example.org")
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				requests := make(chan struct{}, 4)
				ref := SubscriptionRefresher{directDial: subscriptionTestDial(func(_ context.Context, req *http.Request) *http.Response {
					if !strings.HasPrefix(req.Host, "busy.example.org") {
						return subscriptionTestResponse(http.StatusNotFound, "")
					}
					requests <- struct{}{}
					if time.Since(start) > 0 {
						return subscriptionTestResponse(http.StatusOK, string(raw))
					}
					res := subscriptionTestResponse(test.status, "")
					if test.header != nil {
						res.Header.Set("Retry-After", test.header(start))
					}
					return res
				})}
				tunnel := subscriptionTestDial(func(_ context.Context, req *http.Request) *http.Response {
					if strings.HasPrefix(req.Host, "busy.example.org") {
						t.Error("tunnel retried the busy source")
						return subscriptionTestResponse(test.status, "")
					}
					return subscriptionTestResponse(http.StatusOK, string(raw))
				})
				dialers := []SubscriptionDial{tunnel}
				out := ref.Refresh(context.Background(), cfg, dialers)
				sub := out.Config.Subscriptions[0]
				stats := sub.SourceStats[sourceStatsKey(sub.Sources[0])]
				if !out.Changed || sub.ETagSource != sub.Sources[1] || len(requests) != 1 || stats.Direct.Tries != 1 || stats.Tunnel.Tries != 0 || stats.Direct.Fail.Net != 0 {
					t.Fatalf("busy source: %+v, %d requests", stats, len(requests))
				}
				if (test.status == http.StatusTooManyRequests) != (stats.Direct.HTTP429 == 1) || (test.status != http.StatusTooManyRequests) != (stats.Direct.HTTP5xx == 1) || stats.Direct.HTTP429+stats.Direct.HTTP5xx != 1 {
					t.Fatalf("busy answers not counted: 429 %d, 5xx %d", stats.Direct.HTTP429, stats.Direct.HTTP5xx)
				}
				if test.wait > 0 {
					out = ref.Refresh(context.Background(), cfg, nil)
					if out.Changed || len(requests) != 1 {
						t.Fatal("direct ignored Retry-After")
					}
					time.Sleep(test.wait - time.Since(start) - time.Second)
					out = ref.Refresh(context.Background(), cfg, dialers)
					if !out.Changed || len(requests) != 1 {
						t.Fatal("attempt before Retry-After")
					}
				}
				if elapsed := time.Since(start); elapsed < test.wait {
					time.Sleep(test.wait - elapsed)
				}
				out = ref.Refresh(context.Background(), cfg, dialers)
				if !out.Changed || out.Config.Subscriptions[0].ETagSource != sub.Sources[0] || len(requests) != 2 {
					t.Fatal("busy source was skipped after Retry-After")
				}
			})
		})
	}
}

func TestSubscriptionDirectFailureMarkNeedsATunnel(t *testing.T) {
	cfg, raw := subscriptionTestConfig(t, "http://example.org")
	synctest.Test(t, func(t *testing.T) {
		directDials := 0
		ok := subscriptionTestDial(func(context.Context, *http.Request) *http.Response {
			return subscriptionTestResponse(http.StatusOK, string(raw))
		})
		ref := SubscriptionRefresher{directDial: func(ctx context.Context, network, address string) (net.Conn, error) {
			directDials++
			if directDials == 1 {
				return nil, errors.New("connect failed")
			}
			return ok(ctx, network, address)
		}}
		if out := ref.Refresh(context.Background(), cfg, nil); out.Changed {
			t.Fatal("the first refresh should fail")
		}
		out := ref.Refresh(context.Background(), cfg, nil)
		if !out.Changed || directDials != 2 {
			t.Fatalf("no tunnel: the mark kept the only way closed (changed %v, direct dials %d)", out.Changed, directDials)
		}
	})
}

func TestSubscriptionSilentDirectIsMarkedWhenTheTunnelWins(t *testing.T) {
	cfg, raw := subscriptionTestConfig(t, "http://example.org")
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		directDials, tunnelDials := 0, 0
		ok := subscriptionTestDial(func(context.Context, *http.Request) *http.Response {
			return subscriptionTestResponse(http.StatusOK, string(raw))
		})
		ref := SubscriptionRefresher{directDial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			directDials++
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		tunnel := func(ctx context.Context, network, address string) (net.Conn, error) {
			tunnelDials++
			return ok(ctx, network, address)
		}
		out := ref.Refresh(context.Background(), cfg, []SubscriptionDial{tunnel})
		synctest.Wait()
		if !out.Changed || time.Since(start) != 20*time.Second || directDials != 1 || tunnelDials != 1 {
			t.Fatalf("first refresh: changed %v at %v, direct %d, tunnel %d", out.Changed, time.Since(start), directDials, tunnelDials)
		}
		before := time.Now()
		cfg.Subscriptions[0].Revision = 0
		out = ref.Refresh(context.Background(), cfg, []SubscriptionDial{tunnel})
		synctest.Wait()
		if !out.Changed || time.Since(before) != 0 || directDials != 1 || tunnelDials != 2 {
			t.Fatalf("second refresh waited again: changed %v after %v, direct %d, tunnel %d", out.Changed, time.Since(before), directDials, tunnelDials)
		}
	})
}

func TestSubscriptionMarkedDirectIsTriedWhenTheTunnelFails(t *testing.T) {
	cfg, raw := subscriptionTestConfig(t, "http://example.org")
	synctest.Test(t, func(t *testing.T) {
		directDials, tunnelDials := 0, 0
		ok := subscriptionTestDial(func(context.Context, *http.Request) *http.Response {
			return subscriptionTestResponse(http.StatusOK, string(raw))
		})
		ref := SubscriptionRefresher{directDial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if directDials++; directDials == 1 {
				return nil, errors.New("connect failed")
			}
			return ok(ctx, network, address)
		}}
		deadTunnel := func(context.Context, string, string) (net.Conn, error) {
			tunnelDials++
			return nil, errors.New("tunnel down")
		}
		if out := ref.Refresh(context.Background(), cfg, []SubscriptionDial{deadTunnel}); out.Changed {
			t.Fatal("the first refresh should fail")
		}
		// The source is marked and the tunnel is down: the direct way is still tried.
		out := ref.Refresh(context.Background(), cfg, []SubscriptionDial{deadTunnel})
		if !out.Changed || directDials != 2 || tunnelDials != 2 {
			t.Fatalf("changed %v, direct dials %d, tunnel dials %d", out.Changed, directDials, tunnelDials)
		}
	})
}

func TestSubscriptionBusyTunnelAnswerDoesNotSendTheDeferredDirectRequest(t *testing.T) {
	cfg, _ := subscriptionTestConfig(t, "http://example.org")
	synctest.Test(t, func(t *testing.T) {
		directDials := 0
		ref := SubscriptionRefresher{directDial: func(context.Context, string, string) (net.Conn, error) {
			directDials++
			return nil, errors.New("connect failed")
		}}
		busy := subscriptionTestDial(func(context.Context, *http.Request) *http.Response {
			return subscriptionTestResponse(http.StatusTooManyRequests, "")
		})
		// The first refresh marks the source; the second goes to the tunnel, which says busy.
		ref.Refresh(context.Background(), cfg, nil)
		ref.Refresh(context.Background(), cfg, []SubscriptionDial{busy})
		if directDials != 1 {
			t.Fatalf("the busy source was asked directly again: %d direct dials", directDials)
		}
	})
}
