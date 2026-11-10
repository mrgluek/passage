package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

type SubscriptionSourceFailures struct {
	Net     uint32 `json:"net,omitempty"`
	Timeout uint32 `json:"timeout,omitempty"`
	HTTP    uint32 `json:"http,omitempty"`
	Bad     uint32 `json:"bad,omitempty"`
}

type SubscriptionSourceVia struct {
	Direct uint32 `json:"direct,omitempty"`
	Tunnel uint32 `json:"tunnel,omitempty"`
}

type SubscriptionSourceStats struct {
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
	started  bool
	answered bool
	failure  string
	elapsed  time.Duration
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
	case "net", "canceled":
		incrementSourceCounter(&stats.Fail.Net)
	case "timeout":
		incrementSourceCounter(&stats.Fail.Timeout)
	case "http":
		incrementSourceCounter(&stats.Fail.HTTP)
	case "bad":
		incrementSourceCounter(&stats.Fail.Bad)
	}
	s.SourceStats[key] = stats
}

func (s *Subscription) recordSourceOK(source string, tunnel bool, elapsed time.Duration) {
	key := sourceStatsKey(source)
	stats := s.SourceStats[key]
	incrementSourceCounter(&stats.OK)
	if tunnel {
		incrementSourceCounter(&stats.Via.Tunnel)
	} else {
		incrementSourceCounter(&stats.Via.Direct)
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
