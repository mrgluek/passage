package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type SubscriptionSourceFailures struct {
	Net uint32 `json:"net,omitempty"`
	// Refused: the exits answered that they do not carry this destination.
	Refused uint32 `json:"refused,omitempty"`
	Timeout uint32 `json:"timeout,omitempty"`
	HTTP    uint32 `json:"http,omitempty"`
	Bad     uint32 `json:"bad,omitempty"`
}

type SubscriptionSourceVia struct {
	Direct uint32 `json:"direct,omitempty"`
	Tunnel uint32 `json:"tunnel,omitempty"`
}

type SubscriptionSourceAttempts struct {
	Tries    uint32                     `json:"tries,omitempty"`
	Answered uint32                     `json:"answered,omitempty"`
	OK       uint32                     `json:"ok,omitempty"`
	HTTP429  uint32                     `json:"http_429,omitempty"`
	HTTP5xx  uint32                     `json:"http_5xx,omitempty"`
	Fail     SubscriptionSourceFailures `json:"fail,omitempty,omitzero"`
}

type SubscriptionSourceStats struct {
	Tunnel   SubscriptionSourceAttempts `json:"tunnel,omitempty,omitzero"`
	Direct   SubscriptionSourceAttempts `json:"direct,omitempty,omitzero"`
	Tries    uint32                     `json:"tries,omitempty"`
	Answered uint32                     `json:"answered,omitempty"`
	OK       uint32                     `json:"ok,omitempty"`
	Fail     SubscriptionSourceFailures `json:"fail,omitempty,omitzero"`
	Via      SubscriptionSourceVia      `json:"via,omitempty,omitzero"`
	LastOK   int64                      `json:"last_ok,omitempty"`
	LastMS   int64                      `json:"last_ms,omitempty"`
}

type SubscriptionSourceStatus struct {
	ID string `json:"id,omitempty"`
	SubscriptionSourceStats
}

type subscriptionFetchStats struct {
	tunnel     bool
	started    bool
	answered   bool
	failure    string
	elapsed    time.Duration
	busyStatus int
	retryAfter time.Time
}

func sourceStatsKey(source string) string {
	sum := sha256.Sum256([]byte(strings.TrimRight(source, "/")))
	return hex.EncodeToString(sum[:])
}

func (s Subscription) retainedSourceStats() map[string]SubscriptionSourceStats {
	var out map[string]SubscriptionSourceStats
	for _, source := range s.Sources {
		key := sourceStatsKey(source)
		if stats, ok := s.SourceStats[key]; ok {
			if out == nil {
				out = make(map[string]SubscriptionSourceStats)
			}
			out[key] = stats
		}
	}
	return out
}

func incrementSourceCounter(n *uint32) {
	if *n < ^uint32(0) {
		(*n)++
	}
}

func (s *Subscription) recordSourceFetch(source string, result subscriptionFetchStats) {
	if s.SourceStats == nil {
		s.SourceStats = make(map[string]SubscriptionSourceStats)
	}
	key := sourceStatsKey(source)
	stats := s.SourceStats[key]
	if result.started {
		incrementSourceCounter(&stats.Tries)
	}
	if result.answered {
		incrementSourceCounter(&stats.Answered)
	}
	switch result.failure {
	case "net":
		incrementSourceCounter(&stats.Fail.Net)
	case "refused":
		incrementSourceCounter(&stats.Fail.Refused)
	case "timeout":
		incrementSourceCounter(&stats.Fail.Timeout)
	case "http":
		incrementSourceCounter(&stats.Fail.HTTP)
	case "bad":
		incrementSourceCounter(&stats.Fail.Bad)
	}
	attempts := &stats.Direct
	if result.tunnel {
		attempts = &stats.Tunnel
	}
	if result.started {
		incrementSourceCounter(&attempts.Tries)
	}
	if result.answered {
		incrementSourceCounter(&attempts.Answered)
	}
	switch result.failure {
	case "net":
		incrementSourceCounter(&attempts.Fail.Net)
	case "refused":
		incrementSourceCounter(&attempts.Fail.Refused)
	case "timeout":
		incrementSourceCounter(&attempts.Fail.Timeout)
	case "http":
		incrementSourceCounter(&attempts.Fail.HTTP)
	case "bad":
		incrementSourceCounter(&attempts.Fail.Bad)
	}
	switch result.busyStatus {
	case 0:
	case http.StatusTooManyRequests:
		incrementSourceCounter(&attempts.HTTP429)
	default:
		incrementSourceCounter(&attempts.HTTP5xx)
	}
	s.SourceStats[key] = stats
}

func (s *Subscription) recordSourceOK(source string, tunnel bool, elapsed time.Duration) {
	key := sourceStatsKey(source)
	stats := s.SourceStats[key]
	incrementSourceCounter(&stats.OK)
	if tunnel {
		incrementSourceCounter(&stats.Via.Tunnel)
		incrementSourceCounter(&stats.Tunnel.OK)
	} else {
		incrementSourceCounter(&stats.Via.Direct)
		incrementSourceCounter(&stats.Direct.OK)
	}
	stats.LastOK, stats.LastMS = time.Now().Unix(), elapsed.Milliseconds()
	s.SourceStats[key] = stats
}

func (s Subscription) sourceStatuses() []SubscriptionSourceStatus {
	var out []SubscriptionSourceStatus
	for _, source := range s.Sources {
		stats, ok := s.SourceStats[sourceStatsKey(source)]
		if !ok || stats == (SubscriptionSourceStats{}) {
			continue
		}
		id := ""
		for _, kind := range []struct {
			name    string
			sources []string
		}{{"bundle", s.BundleSources}, {"invite", s.originalSources()}} {
			for i, candidate := range kind.sources {
				if sourceStatsKey(candidate) == sourceStatsKey(source) {
					id = kind.name + "-" + strconv.Itoa(i+1)
					break
				}
			}
			if id != "" {
				break
			}
		}
		out = append(out, SubscriptionSourceStatus{ID: id, SubscriptionSourceStats: stats})
	}
	return out
}
