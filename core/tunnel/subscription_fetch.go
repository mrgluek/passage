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
}

type SubscriptionRefreshResult struct {
	Config  Config `json:"config"`
	Changed bool   `json:"changed"`
}

// Refresh tries tunnel dialers first, then direct, without exposing transport errors.
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
		sub.LastTry = time.Now().Unix()
		sub.LastError = "network"
		key := networkKey(cfg) + ":" + sub.ID
		last := r.last[key]
		dials := append([]SubscriptionDial(nil), dialers...)
		dials = append(dials, (&net.Dialer{}).DialContext)
		var pairs []subscriptionPair
		for di := range dials {
			for _, source := range sub.Sources {
				pairs = append(pairs, subscriptionPair{source, di})
			}
		}
		// Rotate within each tier, preserving tunnel-first fallback.
		for start, end := 0, len(dialers)*len(sub.Sources); start < len(pairs); start, end = end, len(pairs) {
			for i := start; i < end; i++ {
				if pairs[i] == last {
					pairs[start], pairs[i] = pairs[i], pairs[start]
					break
				}
			}
			if end == len(pairs) {
				break
			}
		}
		for _, pair := range pairs {
			blob, etag, code := fetchSubscription(ctx, sub, pair.source, dials[pair.dial])
			if code == "" {
				id, _ := hex.DecodeString(sub.ID)
				opened, err := links.OpenSubFile(blob, id, sub.ReadKey, ed25519.PublicKey(sub.Signer))
				if err != nil {
					sub.LastError = subscriptionError(err)
					continue
				}
				candidate, err := ApplySubFile(out.Config, sub, opened.Bundle)
				if errors.Is(err, links.ErrNotNewer) {
					code = "unchanged"
				} else if err != nil {
					sub.LastError = subscriptionError(err)
					continue
				} else {
					out.Config = candidate
					sub = candidate.Subscriptions[index]
					out.Changed = true
				}
			}
			if code != "" && code != "unchanged" {
				sub.LastError = code
				continue
			}
			if code == "unchanged" {
				sub.LastError = "unchanged"
			}
			sub.LastOK = time.Now().Unix()
			sub.ETag, sub.ETagSource = etag, pair.source
			if r.last == nil {
				r.last = map[string]subscriptionPair{}
			}
			if len(r.last) >= 128 {
				clear(r.last)
			}
			r.last[key] = pair
			break
		}
		out.Config.Subscriptions[index] = sub
	}
	return out
}

func fetchSubscription(ctx context.Context, sub Subscription, source string, dial SubscriptionDial) ([]byte, string, string) {
	u, err := url.Parse(source)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(subscriptionTestHTTP && u.Scheme == "http")) || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(source, "#") || strings.Contains(source, "\\") {
		return nil, "", "http"
	}
	id, err := hex.DecodeString(sub.ID)
	if err != nil || len(id) != 16 {
		return nil, "", "decrypt"
	}
	address := strings.TrimRight(source, "/") + "/" + sub.ID + ".bin"
	attempt, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: dial, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(attempt, http.MethodGet, address, nil)
	if err != nil {
		return nil, "", "http"
	}
	if sub.ETag != "" && sub.ETagSource == source {
		req.Header.Set("If-None-Match", sub.ETag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "network"
	}
	defer resp.Body.Close()
	etag := resp.Header.Get("ETag")
	if len(etag) > 4096 {
		etag = ""
	}
	if resp.StatusCode == http.StatusNotModified {
		if sub.ETag == "" || sub.ETagSource != source || sub.Revision == 0 {
			return nil, "", "http"
		}
		if etag == "" {
			etag = sub.ETag
		}
		return nil, etag, "unchanged"
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", "http"
	}
	limit := int64(links.MaxSubFileBytes + links.SubFileOverhead)
	if resp.ContentLength > limit {
		return nil, "", "size"
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", "network"
	}
	if int64(len(raw)) > limit {
		return nil, "", "size"
	}
	return raw, etag, ""
}
