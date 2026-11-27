package tunnel

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	siteLogEvery    = time.Minute // per listed site, and the summary
	siteLogMaxHosts = 256
	siteLogMaxLines = 20 // failure lines per minute for all sites together
	siteNoReply     = "no reply"
)

// siteLog is the browser port's diagnostics: a rate-limited line per failed
// connection and a summary per minute. Site names never appear: a host shows
// as a short hash with a key that lives only as long as the process.
type siteLog struct {
	mu          sync.Mutex
	salt        [16]byte
	last        map[string]time.Time
	suppressed  map[string]int
	ok, failed  int
	lines, held int // failure lines written and held back this minute
	socksErrors map[byte]int
	since       time.Time
}

func newSiteLog(now time.Time) *siteLog {
	l := &siteLog{last: map[string]time.Time{}, suppressed: map[string]int{}, socksErrors: map[byte]int{}, since: now}
	rand.Read(l.salt[:])
	return l
}

// mask is "site-" and four hex digits of a salted hash: the same host gives
// the same label within one run, and the label cannot be matched to a name.
func (l *siteLog) mask(host string) string {
	sum := sha256.Sum256(append(l.salt[:], strings.ToLower(host)...))
	return "site-" + hex.EncodeToString(sum[:2])
}

// allow reports whether a line for key may be written now, and how many were
// held back since the last one.
func (l *siteLog) allow(key string, now time.Time) (bool, int) {
	if at, ok := l.last[key]; ok && now.Sub(at) < siteLogEvery {
		l.suppressed[key]++
		return false, 0
	}
	if len(l.last) >= siteLogMaxHosts {
		clear(l.last)
		clear(l.suppressed)
	}
	l.last[key] = now
	n := l.suppressed[key]
	delete(l.suppressed, key)
	return true, n
}

func suffix(n int) string {
	if n == 0 {
		return ""
	}
	return " (+" + itoa(n) + " suppressed)"
}

func itoa(n int) string { return strconv.Itoa(n) }

// siteFailStage names why a site connection failed.
func siteFailStage(err error) string {
	switch {
	case errors.Is(err, errDestinationRefused):
		return "refused by exit"
	case err == nil:
		return "no route"
	case err.Error() == "all configured routes failed":
		return "all exits failed"
	case err.Error() == "no route available":
		return "no route"
	}
	return transportStage(err)
}

func viaName(tunnel bool) string {
	if tunnel {
		return "tunnel"
	}
	return "smart"
}

// failed records one failed site connection. what is the stage or event,
// tried the routes attempted, took the time spent.
// Lines and labels go by the listed site, not the requested subdomain, so
// a site with many subdomains gets one allowance and one label.
func (t *Tunnel) siteFailed(entry *siteEntry, host, what string, tried []Route, took time.Duration) {
	l := t.siteDiag()
	now := t.timeNow()
	key := entry.name
	if key == "" {
		key = host
	}
	l.mu.Lock()
	l.failed++
	if what == siteNoReply && l.ok > 0 {
		l.ok-- // it was counted as connected a moment ago
	}
	ok, n := l.allow(key, now)
	if ok && l.lines >= siteLogMaxLines {
		ok = false
		l.held++
	} else if ok {
		l.lines++
	}
	label := l.mask(key)
	l.mu.Unlock()
	if !ok {
		return
	}
	names := make([]string, len(tried))
	for i, r := range tried {
		names[i] = r.String()
	}
	t.log.add("site %s (%s) failed: %s; tried %s; %d ms%s", label, viaName(entry.tunnel), what, strings.Join(names, ","), took.Milliseconds(), suffix(n))
}

func (t *Tunnel) siteSucceeded() {
	l := t.siteDiag()
	l.mu.Lock()
	l.ok++
	l.mu.Unlock()
}

// siteSocksError notes a SOCKS error reply to the browser: Chromium may mark
// the proxy bad after one and go direct for minutes. No host is named.
func (t *Tunnel) siteSocksError(code byte, why string) {
	l := t.siteDiag()
	now := t.timeNow()
	l.mu.Lock()
	l.socksErrors[code]++
	ok, n := l.allow("socks:"+why, now)
	l.mu.Unlock()
	if ok {
		t.log.add("browser port: SOCKS error reply 0x%02x (%s)%s", code, why, suffix(n))
	}
}

// flushSiteSummary writes the per-minute line when there was traffic.
func (t *Tunnel) flushSiteSummary(force bool) {
	l := t.siteDiag()
	now := t.timeNow()
	l.mu.Lock()
	if !force && now.Sub(l.since) < siteLogEvery {
		l.mu.Unlock()
		return
	}
	ok, failed, errs, since, held := l.ok, l.failed, 0, l.since, l.held
	for _, n := range l.socksErrors {
		errs += n
	}
	l.ok, l.failed, l.since, l.lines, l.held = 0, 0, now, 0, 0
	clear(l.socksErrors)
	l.mu.Unlock()
	if ok+failed+errs > 0 {
		heldNote := ""
		if held > 0 {
			heldNote = ", " + itoa(held) + " failure lines held back"
		}
		t.log.add("browser port, last %d s: %d connections ok, %d failed, %d SOCKS errors%s", int(now.Sub(since).Seconds()), ok, failed, errs, heldNote)
	}
}

func (t *Tunnel) siteDiag() *siteLog {
	t.siteDiagOnce.Do(func() { t.siteDiagLog = newSiteLog(t.timeNow()) })
	return t.siteDiagLog
}

// siteSummaryLoop runs while the browser listener does.
func (t *Tunnel) siteSummaryLoop() {
	defer t.wg.Done()
	tick := time.NewTicker(siteLogEvery)
	defer tick.Stop()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-tick.C:
			t.flushSiteSummary(true)
		}
	}
}
