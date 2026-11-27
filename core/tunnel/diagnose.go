package tunnel

import (
	"context"
	"errors"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Diagnosis is one network test: every target checked on every way.
type Diagnosis struct {
	Time   time.Time    `json:"time"`
	Ways   []string     `json:"ways"` // column names: direct, each strategy, each tunnel route
	Rows   []DiagRow    `json:"rows"`
	Routes []string     `json:"routes"` // set-up routes
	Checks []RouteCheck `json:"checks,omitempty"`
}

// DiagRow is one target's results, in the order of Diagnosis.Ways.
type DiagRow struct {
	Target  string       `json:"target"`
	Kind    string       `json:"kind"` // relay, canary, reference, telegram or site
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
	type target struct {
		addr, kind string
		site       *siteEntry // a listed site: addr is its label, the name stays here
	}
	ipv6 := t.hasIPv6Route()
	t.mu.Lock()
	var targets []target
	names := make([]string, 0, len(t.relays))
	for n := range t.relays {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, hp := range t.targetsLocked(t.relays[n]) {
			targets = append(targets, target{addr: hp, kind: "relay"})
		}
	}
	for _, c := range t.cfg.Canaries {
		targets = append(targets, target{addr: c, kind: "canary"})
	}
	for _, r := range t.cfg.References {
		targets = append(targets, target{addr: r, kind: "reference"})
	}
	for _, dc := range diagTelegramDCs {
		targets = append(targets, target{addr: dc, kind: "telegram"})
	}
	for _, e := range t.diagSitesLocked() {
		label := t.siteDiag().mask(e.name) + " (" + viaName(e.tunnel) + ")"
		targets = append(targets, target{addr: label, kind: "site", site: e})
	}
	strategies := t.strategies
	var tunnels []tunnelWay
	if p, ok := t.client.(*routePool); ok {
		tunnels = p.ways()
	} else if dial := t.ways[RouteTunnel].dial; dial != nil {
		tunnels = []tunnelWay{{name: "soar", dial: dial, ipv6: t.cfg.TunnelIPv6}}
	}
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
	tunnelCol := len(d.Ways)
	for _, w := range tunnels {
		d.Ways = append(d.Ways, w.name)
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
		if tg.site != nil {
			t.diagSite(d.Rows[i].Results, tg.site, strategies, tunnels, tunnelCol, direct, via, check)
			continue
		}
		host, _, _ := net.SplitHostPort(tg.addr)
		telegram := tg.kind == "telegram"
		skipDirect := !ipv6 && isIPv6Address(tg.addr)
		if skipDirect {
			d.Rows[i].Results[0] = DiagResult{Skip: true}
		} else if telegram {
			check(0, probeTimeout, func(c context.Context) error { return directMT(c, tg.addr) })
		} else {
			check(0, probeTimeout, func(c context.Context) error { return direct(c, tg.addr, host) })
		}
		for j, s := range strategies {
			if telegram || skipDirect {
				d.Rows[i].Results[1+j] = DiagResult{Skip: true}
				continue
			}
			check(1+j, probeTimeout, func(c context.Context) error { return via(c, s.dial, tg.addr, host) })
		}
		for k, w := range tunnels {
			col := tunnelCol + k
			if isIPv6Address(tg.addr) && !w.ipv6 {
				d.Rows[i].Results[col] = DiagResult{Skip: true}
			} else if telegram {
				check(col, 15*time.Second, func(c context.Context) error { return probeMTProtoVia(c, w.dial, tg.addr) })
			} else {
				check(col, 15*time.Second, func(c context.Context) error { return probeTLSVia(c, w.dial, tg.addr, host) })
			}
		}
	}
	data := dataTargets(d.Rows)
	for _, w := range tunnels {
		if w.server.addr == "" {
			continue
		}
		i := len(d.Checks)
		d.Checks = append(d.Checks, RouteCheck{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			d.Checks[i] = checkRoute(cctx, w.name, w.server, w.dial, data)
			d.Checks[i].Exit = w.exit
		}()
	}
	wg.Wait()
	t.log.add("network test: %d targets, %d ways", len(d.Rows), len(d.Ways))
	return d
}

// dataTargets lists where the data stage can fetch from, those on port 443
// first, then Telegram.
func dataTargets(rows []DiagRow) []dataTarget {
	var page, other, telegram []dataTarget
	for _, r := range rows {
		_, port, _ := net.SplitHostPort(r.Target)
		switch {
		case r.Kind == "site":
			// Not an address; the data stage never fetches from a listed site.
		case r.Kind == "telegram":
			telegram = append(telegram, dataTarget{r.Target, true})
		case port == "443":
			page = append(page, dataTarget{r.Target, false})
		default:
			other = append(other, dataTarget{r.Target, false})
		}
	}
	return slices.Concat(page, other, telegram)
}

// shortError keeps the part of an error a person can act on.
func shortError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(err.Error(), "connection refused"), strings.Contains(err.Error(), "actively refused"):
		return "refused"
	case strings.Contains(err.Error(), "connection reset"), strings.Contains(err.Error(), "forcibly closed"):
		return "reset"
	case strings.Contains(err.Error(), "i/o timeout"):
		return "timeout"
	case strings.Contains(err.Error(), "no such host"):
		return "no such host"
	case probeReachable(err):
		return "answered, untrusted chain"
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

// diagSitesLocked picks one host per listed site group, the group's first,
// so the test stays short however long the list is.
func (t *Tunnel) diagSitesLocked() []*siteEntry {
	if t.sites == nil {
		return nil
	}
	var out []*siteEntry
	for _, g := range t.cfg.SiteGroups() {
		if len(g.Hosts) == 0 {
			continue
		}
		if e := t.sites.entries[g.Hosts[0]]; e != nil {
			out = append(out, e)
		}
	}
	return out
}

// diagSite checks a listed site on the ways its kind allows: the name is
// resolved as site connections resolve it (DoT for direct and smart; the
// exit resolves on the tunnel).
func (t *Tunnel) diagSite(res []DiagResult, e *siteEntry, strategies []strategy, tunnels []tunnelWay, tunnelCol int,
	direct func(context.Context, string, string) error, via func(context.Context, dialFunc, string, string) error,
	check func(int, time.Duration, func(context.Context) error)) {
	host := e.name
	resolve := func(c context.Context) (string, error) {
		addrs, err := t.siteResolver().lookup(c, host, !e.tunnel)
		if err != nil {
			return "", err
		}
		addrs = addrsIPv4First(addrs)
		if len(addrs) == 0 {
			return "", errors.New("no addresses")
		}
		return net.JoinHostPort(addrs[0], "443"), nil
	}
	check(0, probeTimeout, func(c context.Context) error {
		addr, err := resolve(c)
		if err != nil {
			return err
		}
		return direct(c, addr, host)
	})
	for j, s := range strategies {
		check(1+j, probeTimeout, func(c context.Context) error {
			addr, err := resolve(c)
			if err != nil {
				return err
			}
			return via(c, s.dial, addr, host)
		})
	}
	for k, w := range tunnels {
		if !e.tunnel {
			res[tunnelCol+k] = DiagResult{Skip: true}
			continue
		}
		check(tunnelCol+k, 15*time.Second, func(c context.Context) error {
			return probeTLSVia(c, w.dial, net.JoinHostPort(host, "443"), host)
		})
	}
}
