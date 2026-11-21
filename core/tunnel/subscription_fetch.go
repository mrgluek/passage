package tunnel

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
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

const (
	subscriptionTunnelTimeout = 20 * time.Second
	subscriptionDirectStagger = 2 * time.Second
	subscriptionTunnelDelay   = 20 * time.Second
	subscriptionDirectBackoff = 30 * time.Minute
	subscriptionMaxRetryAfter = time.Hour
)

type subscriptionNetworkSource struct {
	network string
	source  string
}

// SubscriptionRefresher remembers successful pairs and source backoffs.
type SubscriptionRefresher struct {
	mu           sync.Mutex
	last         map[string]subscriptionPair
	directFailed map[subscriptionNetworkSource]time.Time
	retryAfter   map[string]time.Time

	directTimeout time.Duration
	directDial    SubscriptionDial
}

type SubscriptionRefreshResult struct {
	Config  Config `json:"config"`
	Changed bool   `json:"changed"`
}

type subscriptionFetchResult struct {
	stats      subscriptionFetchStats
	pair       subscriptionPair
	blob       []byte
	etag, code string
}

// Refresh staggers direct requests before starting the tunnel race.
func (r *SubscriptionRefresher) Refresh(ctx context.Context, cfg Config, dialers []SubscriptionDial) SubscriptionRefreshResult {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	maps.DeleteFunc(r.directFailed, func(_ subscriptionNetworkSource, until time.Time) bool { return !now.Before(until) })
	maps.DeleteFunc(r.retryAfter, func(_ string, until time.Time) bool { return !now.Before(until) })
	cfg.Subscriptions = slices.Clone(cfg.Subscriptions)
	out := SubscriptionRefreshResult{Config: cfg}
	busy := make(map[string]bool)
	network := networkKey(cfg)
	for index, sub := range cfg.Subscriptions {
		if ctx.Err() != nil {
			break
		}
		if !sub.Closed {
			r.refreshSubscription(ctx, &out, index, dialers, network, busy)
		}
	}
	return out
}

func (r *SubscriptionRefresher) refreshSubscription(ctx context.Context, out *SubscriptionRefreshResult, index int, dialers []SubscriptionDial, network string, busy map[string]bool) {
	sub := out.Config.Subscriptions[index]
	sub.SourceStats = sub.retainedSourceStats()
	started := time.Now()
	sub.LastTry = started.Unix()
	sub.LastError = "network"
	key := network + ":" + sub.ID
	last := r.last[key]
	directIndex := len(dialers)
	direct := r.directDial
	if direct == nil {
		direct = (&net.Dialer{}).DialContext
	}
	available := func(pair subscriptionPair) bool {
		source := strings.TrimRight(pair.source, "/")
		return !busy[source] && !started.Before(r.retryAfter[source])
	}
	tunnelPairs := slices.DeleteFunc(subscriptionPairs(sub.Sources, 0, directIndex, last), func(pair subscriptionPair) bool {
		return !available(pair)
	})
	// Direct sources that failed lately are left for last: tried only if the
	// tunnel tier gets no answer either, so a stale mark cannot lock a refresh out.
	var deferred []subscriptionPair
	directPairs := slices.DeleteFunc(subscriptionPairs(sub.Sources, directIndex, directIndex+1, subscriptionPair{last.source, directIndex}), func(pair subscriptionPair) bool {
		source := strings.TrimRight(pair.source, "/")
		if !available(pair) {
			return true
		}
		if len(tunnelPairs) > 0 && started.Before(r.directFailed[subscriptionNetworkSource{network, source}]) {
			deferred = append(deferred, pair)
			return true
		}
		return false
	})

	race, cancelRace := context.WithCancel(ctx)
	defer cancelRace()
	directTimeout := r.directTimeout
	if directTimeout <= 0 {
		directTimeout = 40 * time.Second
	}
	directCtx, cancelDirect := context.WithTimeout(race, directTimeout)
	defer cancelDirect()
	cancelTunnel := func() {}
	defer func() { cancelTunnel() }()

	results := make(chan subscriptionFetchResult, len(directPairs)+len(tunnelPairs)+len(deferred))
	fetching := sub
	pending := 0
	launch := func(attempt context.Context, pair subscriptionPair, dial SubscriptionDial) {
		pending++
		go func() {
			stats := subscriptionFetchStats{tunnel: pair.dial < directIndex}
			start := time.Now()
			blob, etag, code := fetchSubscriptionWithStats(attempt, fetching, pair.source, dial, &stats)
			stats.elapsed = time.Since(start)
			results <- subscriptionFetchResult{stats, pair, blob, etag, code}
		}()
	}
	var directTimer, tunnelTimer *time.Timer
	var nextDirect, nextTunnel <-chan time.Time
	startTunnel := func() {
		nextTunnel, nextDirect = nil, nil
		var attempt context.Context
		attempt, cancelTunnel = context.WithTimeout(race, subscriptionTunnelTimeout)
		for _, pair := range tunnelPairs {
			launch(attempt, pair, dialers[pair.dial])
		}
	}
	cutoff := started.Add(subscriptionTunnelDelay)
	startDirect := func() {
		pair := directPairs[0]
		directPairs = directPairs[1:]
		launch(directCtx, pair, direct)
		nextDirect = nil
		if len(directPairs) > 0 && time.Until(cutoff) > subscriptionDirectStagger {
			if directTimer == nil {
				directTimer = time.NewTimer(subscriptionDirectStagger)
			} else {
				directTimer.Reset(subscriptionDirectStagger)
			}
			nextDirect = directTimer.C
		}
	}
	if len(directPairs) > 0 {
		startDirect()
		if len(tunnelPairs) > 0 {
			tunnelTimer = time.NewTimer(time.Until(cutoff))
			nextTunnel = tunnelTimer.C
			defer tunnelTimer.Stop()
		}
	} else if len(tunnelPairs) > 0 {
		startTunnel()
	}
	if directTimer != nil {
		defer directTimer.Stop()
	}

	succeeded, tunnelWon := false, false
	// "Unchanged" is an answer, but another source may hold a newer file, so
	// the first one is kept and accepted only if no source delivers one.
	var held *subscriptionFetchResult
	accept := func(result subscriptionFetchResult, code string) {
		pair, stats := result.pair, result.stats
		sub.LastError = code
		sub.LastOK = time.Now().Unix()
		sub.ETag, sub.ETagSource = result.etag, pair.source
		if r.last == nil || len(r.last) >= 128 {
			r.last = make(map[string]subscriptionPair)
		}
		r.last[key] = pair
		succeeded, tunnelWon = true, stats.tunnel
		nextDirect, nextTunnel = nil, nil
		cancelRace()
	}
	record := func(result subscriptionFetchResult) {
		pending--
		pair, stats, code := result.pair, result.stats, result.code
		source := strings.TrimRight(pair.source, "/")
		directKey := subscriptionNetworkSource{network, source}
		sub.recordSourceFetch(pair.source, stats)
		// A direct attempt that got no answer for a few seconds and was cut off
		// when the tunnel won counts as failed too, so the wait is not repeated.
		silent := tunnelWon && stats.failure == "canceled" && !stats.answered && stats.elapsed >= 2*subscriptionDirectStagger
		if !stats.tunnel && (silent || stats.failure == "net" || stats.failure == "timeout" || stats.failure == "refused") {
			if r.directFailed == nil {
				r.directFailed = make(map[subscriptionNetworkSource]time.Time)
			}
			r.directFailed[directKey] = time.Now().Add(subscriptionDirectBackoff)
		}
		if stats.busyStatus != 0 {
			busy[source] = true
			if !stats.retryAfter.IsZero() {
				if r.retryAfter == nil {
					r.retryAfter = make(map[string]time.Time)
				}
				if stats.retryAfter.After(r.retryAfter[source]) {
					r.retryAfter[source] = stats.retryAfter
				}
			}
			if nextTunnel != nil {
				tunnelPairs = slices.DeleteFunc(tunnelPairs, func(pair subscriptionPair) bool {
					return strings.TrimRight(pair.source, "/") == source
				})
				if len(tunnelPairs) == 0 {
					nextTunnel = nil
				}
			}
		}
		if succeeded {
			return
		}
		if code == "" {
			id, _ := hex.DecodeString(sub.ID)
			opened, err := links.OpenSubFile(result.blob, id, sub.ReadKey, ed25519.PublicKey(sub.Signer))
			if err != nil {
				sub.recordSourceFetch(pair.source, subscriptionFetchStats{failure: "bad", tunnel: stats.tunnel})
				sub.LastError = subscriptionError(err)
				return
			}
			candidate, err := ApplySubFile(out.Config, sub, opened.Bundle)
			if errors.Is(err, links.ErrNotNewer) {
				code = "unchanged"
			} else if err != nil {
				sub.recordSourceFetch(pair.source, subscriptionFetchStats{failure: "bad", tunnel: stats.tunnel})
				sub.LastError = subscriptionError(err)
				return
			} else {
				out.Config = candidate
				sourceStats := sub.SourceStats
				sub = candidate.Subscriptions[index]
				sub.SourceStats = sourceStats
				out.Changed = true
			}
		}
		if code != "" && code != "unchanged" {
			sub.LastError = code
			return
		}
		// Any valid answer, even one that is not used, counts as the source working.
		sub.recordSourceOK(pair.source, stats.tunnel, stats.elapsed)
		if !stats.tunnel {
			delete(r.directFailed, directKey)
		}
		if code == "unchanged" {
			if held == nil {
				result.code = code
				held = &result
			}
			// An answer exists: the tunnel is not needed, the other direct sources still run.
			nextTunnel = nil
			return
		}
		accept(result, code)
	}
	drain := func() {
		for pending > 0 {
			select {
			case result := <-results:
				record(result)
			default:
				return
			}
		}
	}
	done := ctx.Done()
	loop := func() {
		for pending > 0 || nextDirect != nil || nextTunnel != nil {
			select {
			case result := <-results:
				record(result)
				// Every direct attempt has ended without an answer: nothing to wait for.
				if !succeeded && pending == 0 && nextDirect == nil && nextTunnel != nil && race.Err() == nil {
					startTunnel()
				}
			case <-nextDirect:
				drain()
				nextDirect = nil
				if !succeeded && directCtx.Err() == nil && time.Now().Before(cutoff) {
					startDirect()
				}
			case <-nextTunnel:
				drain()
				if nextTunnel != nil && race.Err() == nil {
					startTunnel()
				}
				nextTunnel = nil
			case <-done:
				done, nextDirect, nextTunnel = nil, nil, nil
			}
		}
	}
	loop()
	deferred = slices.DeleteFunc(deferred, func(pair subscriptionPair) bool { return !available(pair) })
	if !succeeded && held == nil && len(deferred) > 0 && ctx.Err() == nil {
		directPairs, deferred = deferred, nil
		second, cancelSecond := context.WithTimeout(race, directTimeout)
		defer cancelSecond()
		directCtx = second
		cutoff = time.Now().Add(subscriptionTunnelDelay)
		startDirect()
		loop()
	}
	if !succeeded && held != nil {
		accept(*held, "unchanged")
	}
	sub.SourceStats = sub.retainedSourceStats()
	out.Config.Subscriptions[index] = sub
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
			copy(pairs[1:i+1], pairs[:i])
			pairs[0] = last
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
	// The transport detaches its dial context from request cancellation.
	transport := &http.Transport{DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(attempt, network, address)
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
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			stats.busyStatus = resp.StatusCode
			stats.retryAfter = subscriptionRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
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
	// The exits refuse destinations that are not on their lists: unlike a
	// network failure this says where the source can be reached instead.
	if errors.Is(err, errDestinationRefused) && !errors.Is(err, errDestinationUnreachable) {
		return "refused"
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

func subscriptionRetryAfter(value string, now time.Time) time.Time {
	seconds, err := strconv.ParseUint(value, 10, 64)
	if err == nil || errors.Is(err, strconv.ErrRange) {
		return now.Add(time.Duration(min(seconds, uint64(subscriptionMaxRetryAfter/time.Second))) * time.Second)
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return now.Add(min(date.Sub(now), subscriptionMaxRetryAfter))
	}
	return time.Time{}
}
