package tunnel

import (
	"context"
	"io"
	"net"
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
	if w, ok := e.winner(time.Now()); !ok || w.route != RouteSmart || w.strategy != "tlsfrag" {
		t.Fatalf("%+v %v", w, ok)
	}
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
