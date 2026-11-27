package tunnel

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func switchWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := siteSwitchWait
	siteSwitchWait = d
	t.Cleanup(func() { siteSwitchWait = old })
}

// silentServer accepts the connection, reads what comes and never answers,
// as when DPI drops the ClientHello.
func silentServer(calls *atomic.Int32) dialFunc {
	return func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		a, b := net.Pipe()
		go io.Copy(io.Discard, b)
		return a, nil
	}
}

// recordingEcho answers each read with prefix+data and keeps what it got.
type recordingEcho struct {
	mu    sync.Mutex
	got   []byte
	calls atomic.Int32
}

func (r *recordingEcho) dial(prefix string) dialFunc {
	return func(context.Context, string, string) (net.Conn, error) {
		r.calls.Add(1)
		a, b := net.Pipe()
		go func() {
			buf := make([]byte, 1024)
			for {
				n, err := b.Read(buf)
				if n > 0 {
					r.mu.Lock()
					r.got = append(r.got, buf[:n]...)
					r.mu.Unlock()
					b.Write(append([]byte(prefix), buf[:n]...))
					b.Close() // one answer, as echoDialer
					return
				}
				if err != nil {
					return
				}
			}
		}()
		return a, nil
	}
}

func TestSiteSwitchesToAStrategyWhenDirectNeverAnswers(t *testing.T) {
	switchWait(t, 100*time.Millisecond)
	var d atomic.Int32
	smart := &recordingEcho{}
	tt := newBrowserTunnel(t, testSites, silentServer(&d), echoDialer("t:", new(atomic.Int32)))
	tt.strategies = []strategy{{"tlsfrag", smart.dial("s:")}}
	c, rep := browserSocks(t, tt, "example.org", nil)
	if rep != 0 {
		t.Fatal(rep)
	}
	start := time.Now()
	if got := roundTrip(t, c); got != "s:hi" {
		t.Fatalf("%q", got)
	}
	if took := time.Since(start); took < 100*time.Millisecond || took > 2*time.Second {
		t.Fatalf("switched after %v", took)
	}
	smart.mu.Lock()
	if string(smart.got) != "hi" {
		t.Fatalf("replayed %q", smart.got)
	}
	smart.mu.Unlock()
	e := tt.sites.match("example.org")
	if !e.badUntil(RouteDirect).After(time.Now()) {
		t.Fatal("the silent way was not marked")
	}
	// The next connection starts on the strategy that answered.
	waitFor(t, func() bool {
		w, ok := e.winner(time.Now())
		return ok && w.route == RouteSmart && w.strategy == "tlsfrag"
	})
	c2, _ := browserSocks(t, tt, "example.org", nil)
	if got := roundTrip(t, c2); got != "s:hi" || d.Load() != 1 {
		t.Fatalf("%q, direct dials %d", got, d.Load())
	}
}

func TestSiteThatAnswersDoesNotSwitch(t *testing.T) {
	switchWait(t, 50*time.Millisecond)
	smart := &recordingEcho{}
	tt := newBrowserTunnel(t, testSites, echoDialer("d:", new(atomic.Int32)), echoDialer("t:", new(atomic.Int32)))
	tt.strategies = []strategy{{"tlsfrag", smart.dial("s:")}}
	c, _ := browserSocks(t, tt, "example.org", nil)
	if got := roundTrip(t, c); got != "d:hi" {
		t.Fatalf("%q", got)
	}
	time.Sleep(150 * time.Millisecond)
	if smart.calls.Load() != 0 {
		t.Fatal("switched although the site answered")
	}
}

// After the site's first byte the connection stays where it is, even if
// the site then goes quiet.
func TestSiteNeverSwitchesAfterTheFirstByte(t *testing.T) {
	switchWait(t, 50*time.Millisecond)
	once := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			buf := make([]byte, 64)
			b.Read(buf)
			b.Write([]byte("x"))
			io.Copy(io.Discard, b) // then silence
		}()
		return a, nil
	}
	smart := &recordingEcho{}
	tt := newBrowserTunnel(t, testSites, once, echoDialer("t:", new(atomic.Int32)))
	tt.strategies = []strategy{{"tlsfrag", smart.dial("s:")}}
	c, _ := browserSocks(t, tt, "example.org", nil)
	c.Write([]byte("hi"))
	one := make([]byte, 1)
	if _, err := io.ReadFull(c, one); err != nil || one[0] != 'x' {
		t.Fatal(err)
	}
	c.Write([]byte("more"))
	time.Sleep(200 * time.Millisecond)
	if smart.calls.Load() != 0 {
		t.Fatal("switched after the first byte")
	}
	c.Close()
}

// A tunnel site goes through the strategies, then the tunnel, each with the replay.
func TestTunnelSiteSwitchChainEndsInTheTunnel(t *testing.T) {
	switchWait(t, 50*time.Millisecond)
	var d, s atomic.Int32
	tun := &recordingEcho{}
	tt := newBrowserTunnel(t, testSites, silentServer(&d), tun.dial("t:"))
	tt.strategies = []strategy{{"split", silentServer(&s)}}
	useFakeSiteTimer(tt) // the dial race is not what is tested here
	c, rep := browserSocks(t, tt, "example.net", nil)
	if rep != 0 {
		t.Fatal(rep)
	}
	if got := roundTrip(t, c); got != "t:hi" || s.Load() == 0 {
		t.Fatalf("%q, strategy dials %d", got, s.Load())
	}
	e := tt.sites.match("example.net")
	if !e.strategyBadUntil("split").After(time.Now()) {
		t.Fatal("the silent strategy was not marked")
	}
}

// Sites use the strategies even when the router chose none for the apps.
func TestSitesUseStrategiesWithoutTheRoutersChoice(t *testing.T) {
	smart := &recordingEcho{}
	tt := newBrowserTunnel(t, testSites, failDialer(new(atomic.Int32)), echoDialer("t:", new(atomic.Int32)))
	tt.ways[RouteSmart] = way{}
	tt.strategies = []strategy{{"disorder", smart.dial("s:")}}
	c, rep := browserSocks(t, tt, "example.org", nil)
	if rep != 0 || roundTrip(t, c) != "s:hi" {
		t.Fatal(rep)
	}
}

func stallWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := siteStallWait
	siteStallWait = d
	t.Cleanup(func() { siteStallWait = old })
}

// answerOnce replies to the first request and then never again.
func answerOnce(context.Context, string, string) (net.Conn, error) {
	a, b := net.Pipe()
	go func() {
		buf := make([]byte, 4096)
		b.Read(buf)
		b.Write(make([]byte, 3000))
		io.Copy(io.Discard, b)
	}()
	return a, nil
}

func stallLines(tt *Tunnel) (n int) {
	for _, e := range tt.log.last(50) {
		if strings.Contains(e.Msg, "stalled on") {
			n++
		}
	}
	return
}

func TestSiteStallAfterTheFirstReplyMarksTheWay(t *testing.T) {
	stallWait(t, 50*time.Millisecond)
	tt := newBrowserTunnel(t, testSites, answerOnce, echoDialer("t:", new(atomic.Int32)))
	c, _ := browserSocks(t, tt, "example.org", nil)
	defer c.Close()
	c.Write([]byte("GET / first"))
	io.ReadFull(c, make([]byte, 3000))
	c.Write(make([]byte, 200)) // a second request that never gets an answer
	waitFor(t, func() bool { return tt.sites.match("example.org").badUntil(RouteDirect).After(time.Now()) })
	if stallLines(tt) != 1 {
		t.Fatal("no stall line")
	}
	for _, e := range tt.log.last(50) {
		if strings.Contains(e.Msg, "stalled on") && !strings.Contains(e.Msg, "site #1 (smart) stalled on direct after 2 KB") {
			t.Fatal(e.Msg)
		}
	}
	if _, err := c.Write([]byte("still open")); err != nil {
		t.Fatal("the stalled connection was cut")
	}
}

func TestQuietSiteConnectionIsNoStall(t *testing.T) {
	stallWait(t, 30*time.Millisecond)
	tt := newBrowserTunnel(t, testSites, answerOnce, echoDialer("t:", new(atomic.Int32)))
	c, _ := browserSocks(t, tt, "example.org", nil)
	defer c.Close()
	c.Write([]byte("GET / first"))
	io.ReadFull(c, make([]byte, 3000))
	c.Write(make([]byte, 30)) // an acknowledgement, not a request
	time.Sleep(200 * time.Millisecond)
	if tt.sites.match("example.org").badUntil(RouteDirect).After(time.Now()) || stallLines(tt) != 0 {
		t.Fatal("an idle connection counted as a stall")
	}
}

// answerN replies to the first request with n bytes and then never again.
func answerN(n int) dialFunc {
	return func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			b.Read(make([]byte, 4096))
			b.Write(make([]byte, n))
			io.Copy(io.Discard, b)
		}()
		return a, nil
	}
}

// stallAfter opens a connection to host, takes n reply bytes, sends a second
// request and waits for the stall line.
func stallAfter(t *testing.T, tt *Tunnel, host string, n int) string {
	t.Helper()
	c, rep := browserSocks(t, tt, host, nil)
	if rep != 0 {
		t.Fatal(rep)
	}
	t.Cleanup(func() { c.Close() })
	c.Write([]byte("GET / first"))
	if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	c.Write(make([]byte, 200))
	var line string
	waitFor(t, func() bool {
		for _, e := range tt.log.last(50) {
			if strings.Contains(e.Msg, "stalled on") {
				line = e.Msg
				return true
			}
		}
		return false
	})
	return line
}

func TestTunnelStallIsNotMarked(t *testing.T) {
	stallWait(t, 50*time.Millisecond)
	tt := newBrowserTunnel(t, testSites, failDialer(new(atomic.Int32)), answerN(3000))
	useFakeSiteTimer(tt)
	line := stallAfter(t, tt, "example.net", 3000)
	if !strings.HasSuffix(line, "(not marked)") || tt.sites.match("example.net").badUntil(RouteTunnel).After(time.Now()) {
		t.Fatalf("%q", line)
	}
}

func TestEarlySmartStallIsMarked(t *testing.T) {
	stallWait(t, 50*time.Millisecond)
	tt := newBrowserTunnel(t, testSites, failDialer(new(atomic.Int32)), echoDialer("t:", new(atomic.Int32)))
	tt.strategies = []strategy{{"tlsfrag:1", answerN(20 << 10)}}
	line := stallAfter(t, tt, "example.org", 20<<10)
	if strings.Contains(line, "not marked") || !strings.Contains(line, "stalled on smart:tlsfrag:1 after 20 KB") ||
		!tt.sites.match("example.org").strategyBadUntil("tlsfrag:1").After(time.Now()) {
		t.Fatalf("%q", line)
	}
}

func TestLateQuietIsNotMarked(t *testing.T) {
	stallWait(t, 50*time.Millisecond)
	// After 200 KB.
	tt := newBrowserTunnel(t, testSites, answerN(200<<10), echoDialer("t:", new(atomic.Int32)))
	if line := stallAfter(t, tt, "example.org", 200<<10); !strings.HasSuffix(line, "(not marked)") || tt.sites.match("example.org").badUntil(RouteDirect).After(time.Now()) {
		t.Fatalf("after 200 KB: %q", line)
	}
	// After the early phase (two minutes, shortened here).
	old := siteStallEarlyAge
	siteStallEarlyAge = 10 * time.Millisecond
	t.Cleanup(func() { siteStallEarlyAge = old })
	tt2 := newBrowserTunnel(t, testSites, answerN(3000), echoDialer("t:", new(atomic.Int32)))
	if line := stallAfter(t, tt2, "example.org", 3000); !strings.HasSuffix(line, "(not marked)") || tt2.sites.match("example.org").badUntil(RouteDirect).After(time.Now()) {
		t.Fatalf("late: %q", line)
	}
}

func TestTunnelSiteGoesToTheTunnelAfterAnEarlyStall(t *testing.T) {
	stallWait(t, 50*time.Millisecond)
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	var d, s atomic.Int32
	strat := answerN(20 << 10)
	counted := func(context.Context, string, string) (net.Conn, error) {
		s.Add(1)
		return strat(context.Background(), "", "")
	}
	tu := &recordingEcho{}
	tt := newBrowserTunnel(t, testSites, failDialer(&d), tu.dial("t:"))
	tt.now = func() time.Time { return time.Unix(0, now.Load()) }
	tt.strategies = []strategy{{"tlsfrag:1", counted}}
	clock := useFakeSiteTimer(tt)
	line := stallAfter(t, tt, "example.net", 20<<10)
	if strings.Contains(line, "not marked") {
		t.Fatal(line)
	}
	waitFor(t, func() bool {
		for _, e := range tt.log.last(50) {
			if strings.Contains(e.Msg, "site #2 (tunnel) uses the tunnel for 24 h after a stall on smart:tlsfrag:1") {
				return true
			}
		}
		return false
	})
	// The next connection goes to the tunnel at once.
	d0, s0, timers := d.Load(), s.Load(), len(clock.asked)
	res := <-dialSiteAsync(tt, "example.net")
	if res.err != nil || res.route != RouteTunnel || d.Load() != d0 || s.Load() != s0 || len(clock.asked) != timers {
		t.Fatalf("%+v; direct %d->%d, smart %d->%d", res, d0, d.Load(), s0, s.Load())
	}
	res.conn.Close()
	// 24 hours later the race is back.
	now.Add(int64(siteTunnelOnlyFor + time.Minute))
	tt.sites.match("example.net").clear(RouteDirect)
	res = <-dialSiteAsync(tt, "example.net")
	// The direct attempt runs beside the others: wait for it to show up.
	waitFor(t, func() bool { return d.Load() > d0 })
	if res.conn != nil {
		res.conn.Close()
	}
}

func TestTunnelOnlyEndsOnNetworkChangeAndTunnelFailure(t *testing.T) {
	tt := newBrowserTunnel(t, testSites, failDialer(new(atomic.Int32)), echoDialer("t:", new(atomic.Int32)))
	e := tt.sites.match("example.net")
	e.useTunnelOnly(time.Now())
	tt.NetworkChanged()
	if e.tunnelOnlyNow(time.Now()) {
		t.Fatal("kept after a network change")
	}
	e.useTunnelOnly(time.Now())
	e.markBad(RouteTunnel, time.Now().Add(siteBadFor))
	if e.tunnelOnlyNow(time.Now()) {
		t.Fatal("kept after the tunnel failed")
	}
}

func TestSmartSiteStallKeepsTodaysRule(t *testing.T) {
	stallWait(t, 50*time.Millisecond)
	tt := newBrowserTunnel(t, testSites, failDialer(new(atomic.Int32)), echoDialer("t:", new(atomic.Int32)))
	tt.strategies = []strategy{{"tlsfrag:1", answerN(20 << 10)}}
	stallAfter(t, tt, "example.org", 20<<10)
	e := tt.sites.match("example.org")
	if e.tunnelOnlyNow(time.Now()) || !e.strategyBadUntil("tlsfrag:1").After(time.Now()) {
		t.Fatal("smart site rule changed")
	}
	for _, l := range tt.log.last(50) {
		if strings.Contains(l.Msg, "uses the tunnel") {
			t.Fatal(l.Msg)
		}
	}
}
