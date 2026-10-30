package tunnel

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Diagnosis is one network test: every target checked on every way.
type Diagnosis struct {
	Time   time.Time `json:"time"`
	Ways   []string  `json:"ways"` // column names: direct, each strategy, tunnel
	Rows   []DiagRow `json:"rows"`
	Routes []string  `json:"routes"` // set-up routes
}

// DiagRow is one target's results, in the order of Diagnosis.Ways.
type DiagRow struct {
	Target  string       `json:"target"`
	Kind    string       `json:"kind"` // relay, canary or telegram
	Results []DiagResult `json:"results"`
}

// DiagResult is one check: OK with the time it took, or the error.
type DiagResult struct {
	OK    bool   `json:"ok"`
	MS    int64  `json:"ms,omitempty"`
	Error string `json:"error,omitempty"`
	Skip  bool   `json:"skip,omitempty"` // the way doesn't apply here
}

// diagTelegramDCs are the data centers the test checks.
var diagTelegramDCs = []string{telegramDCs[1], telegramDCs[3]}

// Diagnose checks every relay, canary and two Telegram data centers
// directly, with each smart strategy and through the tunnel, all at once.
// It is the user's own test, so it runs whether or not a route needs it.
func (t *Tunnel) Diagnose(ctx context.Context) Diagnosis {
	type target struct{ addr, kind string }
	t.mu.Lock()
	var targets []target
	names := make([]string, 0, len(t.relays))
	for n := range t.relays {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, hp := range t.targetsLocked(t.relays[n]) {
			targets = append(targets, target{hp, "relay"})
		}
	}
	for _, c := range t.cfg.Canaries {
		targets = append(targets, target{c, "canary"})
	}
	for _, dc := range diagTelegramDCs {
		targets = append(targets, target{dc, "telegram"})
	}
	strategies := t.strategies
	tunnel := t.ways[RouteTunnel].dial
	direct, directMT := t.probeTLS, t.probeMT
	via := t.smartProbe
	if via == nil {
		via = probeTLSVia
	}
	routes := t.routes()
	t.mu.Unlock()

	d := Diagnosis{Time: time.Now().UTC(), Ways: []string{"direct"}, Routes: routes}
	for _, s := range strategies {
		d.Ways = append(d.Ways, s.name)
	}
	if tunnel != nil {
		d.Ways = append(d.Ways, "tunnel")
	}
	d.Rows = make([]DiagRow, len(targets))
	var wg sync.WaitGroup
	for i, tg := range targets {
		d.Rows[i] = DiagRow{Target: tg.addr, Kind: tg.kind, Results: make([]DiagResult, len(d.Ways))}
		check := func(col int, timeout time.Duration, f func(context.Context) error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cctx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				start := time.Now()
				err := f(cctx)
				r := DiagResult{OK: err == nil, MS: time.Since(start).Milliseconds()}
				if err != nil {
					r.MS, r.Error = 0, shortError(err)
				}
				d.Rows[i].Results[col] = r
			}()
		}
		host, _, _ := net.SplitHostPort(tg.addr)
		telegram := tg.kind == "telegram"
		if telegram {
			check(0, probeTimeout, func(c context.Context) error { return directMT(c, tg.addr) })
		} else {
			check(0, probeTimeout, func(c context.Context) error { return direct(c, tg.addr, host) })
		}
		for j, s := range strategies {
			if telegram { // MTProto is not TLS: strategies don't apply
				d.Rows[i].Results[1+j] = DiagResult{Skip: true}
				continue
			}
			check(1+j, probeTimeout, func(c context.Context) error { return via(c, s.dial, tg.addr, host) })
		}
		if tunnel != nil {
			col := len(d.Ways) - 1
			if telegram {
				check(col, 15*time.Second, func(c context.Context) error { return probeMTProtoVia(c, tunnel, tg.addr) })
			} else {
				check(col, 15*time.Second, func(c context.Context) error { return probeTLSVia(c, tunnel, tg.addr, host) })
			}
		}
	}
	wg.Wait()
	t.log.add("network test: %d targets, %d ways", len(d.Rows), len(d.Ways))
	return d
}

// shortError keeps the part of an error a person can act on.
func shortError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(err.Error(), "connection refused"):
		return "refused"
	case strings.Contains(err.Error(), "connection reset"):
		return "reset"
	case strings.Contains(err.Error(), "i/o timeout"):
		return "timeout"
	case strings.Contains(err.Error(), "no such host"):
		return "no such host"
	case strings.Contains(err.Error(), "certificate"):
		return "bad certificate"
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
