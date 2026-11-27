package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
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
	// SitesNotTested counts listed sites beyond diagSiteRows.
	SitesNotTested int `json:"sites_not_tested,omitempty"`
}

// DiagRow is one target's results, in the order of Diagnosis.Ways.
type DiagRow struct {
	Target  string       `json:"target"`
	Kind    string       `json:"kind"` // relay, canary, reference, telegram or site
	Results []DiagResult `json:"results"`
	// Site is a listed site's number (Target is then "site #N (via)"), Via
	// its kind. Host is its name, for the user's own screen only: it never
	// goes into JSON or a report.
	Site int    `json:"site,omitempty"`
	Via  string `json:"via,omitempty"`
	Host string `json:"-"`
}

// diagSiteRows caps the listed sites a network test checks.
const diagSiteRows = 16

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
	sites := t.diagSitesLocked()
	notTested := 0
	if len(sites) > diagSiteRows {
		sites, notTested = sites[:diagSiteRows], len(sites)-diagSiteRows
	}
	for _, e := range sites {
		targets = append(targets, target{addr: e.label(), kind: "site", site: e})
	}
	strategies := t.strategies
	var tunnels []tunnelWay
	if p, ok := t.client.(*routePool); ok {
		tunnels = p.ways()
	} else if dial := t.ways[RouteTunnel].dial; dial != nil {
		tunnels = []tunnelWay{{name: "soar", dial: dial, ipv6: t.cfg.TunnelIPv6}}
	}
	direct, directMT := t.probeTLS, t.probeMT
	directDial := t.ways[RouteDirect].dial
	via := t.smartProbe
	if via == nil {
		via = probeTLSVia
	}
	routes := t.routes()
	t.mu.Unlock()

	d := Diagnosis{Time: time.Now().UTC(), Ways: []string{"direct"}, Routes: routes, SitesNotTested: notTested}
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
		if tg.site != nil {
			d.Rows[i].Site, d.Rows[i].Via = tg.site.num, viaName(tg.site.tunnel)
		}
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
			t.diagSite(&d.Rows[i], tg.site, strategies, tunnels, tunnelCol, directDial, &wg, check)
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
	for i := range rows {
		r := &rows[i] // site rows are still being filled in: read Kind and Target only
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
	var stall *dataStall
	switch {
	case errors.As(err, &stall):
		return stall.Error()
	case errors.Is(err, errSiteDoT):
		return "dot failed"
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

// diagSitesLocked lists every listed site in number order.
func (t *Tunnel) diagSitesLocked() []*siteEntry {
	if t.sites == nil {
		return nil
	}
	out := make([]*siteEntry, 0, len(t.sites.entries))
	for _, e := range t.sites.entries {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b *siteEntry) int { return a.num - b.num })
	return out
}

// diagSite checks a listed site on the ways its kind allows: the name is
// resolved as site connections resolve it (DoT for direct and smart; the
// exit resolves on the tunnel), and each way must carry data, not only a
// handshake. A bare name without an address is tried with "www.".
func (t *Tunnel) diagSite(row *DiagRow, e *siteEntry, strategies []strategy, tunnels []tunnelWay, tunnelCol int,
	directDial dialFunc, wg *sync.WaitGroup, check func(int, time.Duration, func(context.Context) error)) {
	probe := t.siteDataProbe
	if probe == nil {
		probe = probeData
	}
	res := row.Results
	wg.Add(1)
	go func() {
		defer wg.Done()
		host, addr, err := t.diagSiteAddr(e)
		if err != nil && noAddress(err) {
			row.Host = e.name
			res[0] = DiagResult{Error: "no address"}
			for i := 1; i < len(res); i++ {
				res[i] = DiagResult{Skip: true}
			}
			return
		}
		row.Host = host
		lookupErr := err
		dataCheck := func(col int, dial dialFunc) {
			if dial == nil {
				res[col] = DiagResult{Skip: true}
				return
			}
			check(col, probeTimeout+siteDataWait, func(c context.Context) error {
				if lookupErr != nil {
					return lookupErr
				}
				return probe(c, dial, addr, host)
			})
		}
		dataCheck(0, directDial)
		for j, s := range strategies {
			dataCheck(1+j, s.dial)
		}
		for k, w := range tunnels {
			if !e.tunnel {
				res[tunnelCol+k] = DiagResult{Skip: true}
				continue
			}
			check(tunnelCol+k, 15*time.Second+siteDataWait, func(c context.Context) error {
				return probe(c, w.dial, net.JoinHostPort(host, "443"), host)
			})
		}
	}()
}

// diagSiteAddr resolves a listed site for the test, with "www." when the
// bare name has no address.
func (t *Tunnel) diagSiteAddr(e *siteEntry) (host, addr string, err error) {
	ctx, cancel := context.WithTimeout(t.ctx, 2*pinnedDoTLimit)
	defer cancel()
	for _, h := range []string{e.name, "www." + e.name} {
		var addrs []string
		addrs, err = t.siteResolver().lookup(ctx, h, !e.tunnel)
		if err == nil {
			if addrs = addrsIPv4First(addrs); len(addrs) > 0 {
				return h, net.JoinHostPort(addrs[0], "443"), nil
			}
			err = errNoAddress
		}
		if !noAddress(err) {
			return e.name, "", err
		}
	}
	return e.name, "", err
}

var errNoAddress = errors.New("no addresses")

// noAddress: the name exists nowhere (an answer, not a failed lookup).
func noAddress(err error) bool {
	var dnsErr *net.DNSError
	return errors.Is(err, errNoAddress) || errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// siteDataWait bounds the data part of a site check; siteDataLimit is
// what counts as data flowing.
var siteDataWait = 8 * time.Second

const siteDataLimit = 64 << 10

// dataStall is a check that got a handshake but not the data.
type dataStall struct{ got int }

func (e *dataStall) Error() string {
	if e.got == 0 {
		return "no data"
	}
	return fmt.Sprintf("stall %dKB", (e.got+1023)/1024)
}

// probeData opens TLS through dial, asks for "/" and reads: ok when
// siteDataLimit arrives or the reply ends, a dataStall when it stops short.
func probeData(ctx context.Context, dial dialFunc, hostport, sni string) error {
	c, err := dial(ctx, "tcp", hostport)
	if err != nil {
		return err
	}
	defer c.Close()
	tc := tls.Client(c, &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12, RootCAs: probeRoots, NextProtos: []string{"http/1.1"}})
	if err := tc.HandshakeContext(ctx); err != nil {
		return err
	}
	deadline := time.Now().Add(siteDataWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	tc.SetDeadline(deadline)
	if _, err := fmt.Fprintf(tc, "GET / HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0\r\nAccept: */*\r\nConnection: close\r\n\r\n", sni); err != nil {
		return err
	}
	return readData(tc)
}

// readData reads until siteDataLimit or the end of the reply.
func readData(r io.Reader) error {
	buf := make([]byte, 16<<10)
	got := 0
	for got < siteDataLimit {
		n, err := r.Read(buf)
		got += n
		if err == nil {
			continue
		}
		if got >= siteDataLimit || errors.Is(err, io.EOF) && got > 0 {
			return nil
		}
		var ne net.Error
		if got > 0 || errors.As(err, &ne) && ne.Timeout() {
			return &dataStall{got}
		}
		return err
	}
	return nil
}
