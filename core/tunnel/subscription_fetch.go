package tunnel

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

type SubscriptionDial = func(context.Context, string, string) (net.Conn, error)
type subscriptionPair struct {
	source string
	dial   int
}

// SubscriptionRefresher remembers successful source/dialer pairs per network.
type SubscriptionRefresher struct {
	mu   sync.Mutex
	last map[string]subscriptionPair

	tunnelTimeout time.Duration
	directTimeout time.Duration
	directDial    SubscriptionDial
}

type SubscriptionRefreshResult struct {
	Config  Config `json:"config"`
	Changed bool   `json:"changed"`
}

// Refresh races tunnel pairs first, then falls back to direct pairs.
func (r *SubscriptionRefresher) Refresh(ctx context.Context, cfg Config, dialers []SubscriptionDial) SubscriptionRefreshResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg.Subscriptions = append([]Subscription(nil), cfg.Subscriptions...)
	out := SubscriptionRefreshResult{Config: cfg}
	for index, sub := range cfg.Subscriptions {
		if sub.Closed {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		sub.SourceStats = sub.retainedSourceStats()
		sub.LastTry = time.Now().Unix()
		sub.LastError = "network"
		key := networkKey(cfg) + ":" + sub.ID
		last := r.last[key]
		direct := r.directDial
		if direct == nil {
			direct = (&net.Dialer{}).DialContext
		}
		dials := append(append([]SubscriptionDial(nil), dialers...), direct)
		tunnelTimeout, directTimeout := r.tunnelTimeout, r.directTimeout
		if tunnelTimeout <= 0 {
			tunnelTimeout = 20 * time.Second
		}
		if directTimeout <= 0 {
			directTimeout = 40 * time.Second
		}
		type response struct {
			stats      subscriptionFetchStats
			pair       subscriptionPair
			blob       []byte
			etag, code string
		}
		succeeded := false
		failedTunnel := map[string]bool{}
		successfulTunnel := map[string]bool{}
		for _, tier := range []struct {
			first, end int
			timeout    time.Duration
		}{{0, len(dialers), tunnelTimeout}, {len(dialers), len(dials), directTimeout}} {
			diagnostic := succeeded && tier.first == len(dialers)
			if ctx.Err() != nil || tier.first == tier.end {
				continue
			}
			pairs := subscriptionPairs(sub.Sources, tier.first, tier.end, last)
			if diagnostic {
				pairs = nil
				now := time.Now().Unix()
				for _, source := range sub.Sources {
					stats := sub.SourceStats[sourceStatsKey(source)]
					if failedTunnel[source] && !successfulTunnel[source] && (stats.LastDirectCheck == 0 || now-stats.LastDirectCheck >= 86400) {
						pairs = append(pairs, subscriptionPair{source, len(dialers)})
						stats.LastDirectCheck = now
						sub.SourceStats[sourceStatsKey(source)] = stats
					}
				}
			}
			if !diagnostic && tier.first == len(dialers) {
				for _, pair := range pairs {
					if failedTunnel[pair.source] {
						stats := sub.SourceStats[sourceStatsKey(pair.source)]
						stats.LastDirectCheck = time.Now().Unix()
						sub.SourceStats[sourceStatsKey(pair.source)] = stats
					}
				}
			}
			attempt, cancel := context.WithTimeout(ctx, tier.timeout)
			results := make(chan response, len(pairs))
			var workers sync.WaitGroup
			fetching := sub
			for _, pair := range pairs {
				workers.Go(func() {
					var stats subscriptionFetchStats
					start := time.Now()
					blob, etag, code := fetchSubscriptionWithStats(attempt, fetching, pair.source, dials[pair.dial], &stats)
					stats.elapsed = time.Since(start)
					stats.tunnel = pair.dial < len(dialers)
					results <- response{stats, pair, blob, etag, code}
				})
			}
			accept := func(result response) {
				sub.LastError = result.code
				sub.LastOK = time.Now().Unix()
				sub.ETag, sub.ETagSource = result.etag, result.pair.source
				if r.last == nil {
					r.last = map[string]subscriptionPair{}
				}
				if len(r.last) >= 128 {
					clear(r.last)
				}
				r.last[key] = result.pair
				succeeded = true
			}
			var unchanged *response
			for range pairs {
				result := <-results
				pair, blob, code := result.pair, result.blob, result.code
				if result.stats.tunnel && result.stats.failure != "" && result.stats.failure != "canceled" {
					failedTunnel[pair.source] = true
				}
				sub.recordSourceFetch(pair.source, result.stats)
				if succeeded && !diagnostic {
					continue
				}
				if code == "" {
					id, _ := hex.DecodeString(sub.ID)
					opened, err := links.OpenSubFile(blob, id, sub.ReadKey, ed25519.PublicKey(sub.Signer))
					if err != nil {
						sub.recordSourceFetch(pair.source, subscriptionFetchStats{failure: "bad", tunnel: pair.dial < len(dialers)})
						if pair.dial < len(dialers) {
							failedTunnel[pair.source] = true
						}
						if !diagnostic {
							sub.LastError = subscriptionError(err)
						}
						continue
					}
					candidate, err := ApplySubFile(out.Config, sub, opened.Bundle)
					if errors.Is(err, links.ErrNotNewer) {
						code = "unchanged"
					} else if err != nil {
						sub.recordSourceFetch(pair.source, subscriptionFetchStats{failure: "bad", tunnel: pair.dial < len(dialers)})
						if pair.dial < len(dialers) {
							failedTunnel[pair.source] = true
						}
						if !diagnostic {
							sub.LastError = subscriptionError(err)
						}
						continue
					} else if !diagnostic {
						out.Config = candidate
						stats := sub.SourceStats
						sub = candidate.Subscriptions[index]
						sub.SourceStats = stats
						out.Changed = true
					}
				}
				if code != "" && code != "unchanged" {
					if !diagnostic {
						sub.LastError = code
					}
					continue
				}
				sub.recordSourceOK(pair.source, pair.dial < len(dialers), result.stats.elapsed)
				if pair.dial < len(dialers) {
					successfulTunnel[pair.source] = true
				}
				if diagnostic {
					continue
				}
				result.code = code
				if code == "unchanged" {
					if unchanged == nil {
						unchanged = &result
					}
					continue
				}
				accept(result)
				cancel()
			}
			if !succeeded && unchanged != nil {
				accept(*unchanged)
			}
			cancel()
			workers.Wait()
		}
		sub.SourceStats = sub.retainedSourceStats()
		out.Config.Subscriptions[index] = sub
	}
	return out
}

func subscriptionPairs(sources []string, first, end int, last subscriptionPair) []subscriptionPair {
	var pairs []subscriptionPair
	for di := first; di < end; di++ {
		for _, source := range sources {
			pairs = append(pairs, subscriptionPair{source, di})
		}
	}
	for i := range pairs {
		if pairs[i] == last {
			pairs[0], pairs[i] = pairs[i], pairs[0]
			break
		}
	}
	return pairs
}

func fetchSubscription(ctx context.Context, sub Subscription, source string, dial SubscriptionDial) ([]byte, string, string) {
	return fetchSubscriptionWithStats(ctx, sub, source, dial, &subscriptionFetchStats{})
}

func fetchSubscriptionWithStats(ctx context.Context, sub Subscription, source string, dial SubscriptionDial, stats *subscriptionFetchStats) ([]byte, string, string) {
	u, err := url.Parse(source)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(subscriptionTestHTTP && u.Scheme == "http")) || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(source, "#") || strings.Contains(source, "\\") {
		stats.failure = "http"
		return nil, "", "http"
	}
	id, err := hex.DecodeString(sub.ID)
	if err != nil || len(id) != 16 {
		stats.failure = "bad"
		return nil, "", "decrypt"
	}
	address := strings.TrimRight(source, "/") + "/" + sub.ID + ".bin"
	attempt, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	var answered atomic.Bool
	defer func() { stats.answered = stats.answered || answered.Load() }()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &subscriptionResponseConn{Conn: conn, answered: &answered}, nil
	}, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(attempt, http.MethodGet, address, nil)
	if err != nil {
		stats.failure = "http"
		return nil, "", "http"
	}
	if sub.ETag != "" && sub.ETagSource == source {
		req.Header.Set("If-None-Match", sub.ETag)
	}
	stats.started = true
	resp, err := client.Do(req)
	stats.answered = resp != nil
	if err != nil {
		if errors.Is(attempt.Err(), context.Canceled) {
			stats.failure = "canceled"
		} else {
			stats.failure = subscriptionNetworkFailure(err)
		}
		return nil, "", "network"
	}
	defer resp.Body.Close()
	etag := resp.Header.Get("ETag")
	if len(etag) > 4096 {
		etag = ""
	}
	if resp.StatusCode == http.StatusNotModified {
		if sub.ETag == "" || sub.ETagSource != source || sub.Revision == 0 {
			stats.failure = "http"
			return nil, "", "http"
		}
		if etag == "" {
			etag = sub.ETag
		}
		return nil, etag, "unchanged"
	}
	if resp.StatusCode != http.StatusOK {
		stats.failure = "http"
		return nil, "", "http"
	}
	limit := int64(links.MaxSubFileBytes + links.SubFileOverhead)
	if resp.ContentLength > limit {
		stats.failure = "bad"
		return nil, "", "size"
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if errors.Is(attempt.Err(), context.Canceled) {
			stats.failure = "canceled"
		} else {
			stats.failure = subscriptionNetworkFailure(err)
		}
		return nil, "", "network"
	}
	if int64(len(raw)) > limit {
		stats.failure = "bad"
		return nil, "", "size"
	}
	return raw, etag, ""
}

func subscriptionNetworkFailure(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	return "net"
}

type subscriptionResponseConn struct {
	net.Conn
	answered *atomic.Bool
}

func (c *subscriptionResponseConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.answered.Store(true)
	}
	return n, err
}
