package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func evidenceTunnel(now *time.Time) (*Tunnel, *group) {
	g := newGroup("relay.example.org", kindRelay, RouteDirect)
	t := &Tunnel{log: newEventLog(100), now: func() time.Time { return *now },
		relays: map[string]*group{g.name: g}, telegram: newGroup("telegram", kindTelegram, RouteDirect),
		other: newGroup("other", kindOther, RouteDirect), ctx: context.Background()}
	return t, g
}

func disagreementLines(t *Tunnel) []string {
	var lines []string
	for _, event := range t.log.last(100) {
		if strings.HasPrefix(event.Msg, "disagree:") {
			lines = append(lines, event.Msg)
		}
	}
	return lines
}

func TestConnectionProbeDisagreement(t *testing.T) {
	for _, outcome := range []string{"ok", "stall", "refused", "timeout", "later"} {
		for _, probeOK := range []bool{false, true} {
			for _, probeFirst := range []bool{false, true} {
				t.Run(outcome+"/probe="+stringBool(probeOK)+"/first="+stringBool(probeFirst), func(t *testing.T) {
					now := time.Unix(1000, 0)
					tt, g := evidenceTunnel(&now)
					probe := func() {
						r := probeResult{Relays: 1}
						if probeOK {
							r.RelayOK = 1
						}
						tt.applyProbe(g, r)
					}
					conn := func() { tt.recordConnection(g, outcome, "route-a") }
					if probeFirst {
						probe()
						conn()
					} else {
						conn()
						probe()
					}
					lines := disagreementLines(tt)
					want := probeOK && (outcome == "stall" || outcome == "refused" || outcome == "timeout") || !probeOK && outcome == "ok"
					if !want {
						if len(lines) != 0 {
							t.Fatal(lines)
						}
						return
					}
					verdict := "failed"
					if probeOK {
						verdict = "ok"
					}
					expected := "disagree: " + g.historyUnit() + " direct probe=" + verdict + " conn=" + outcome + " via route-a"
					if len(lines) != 1 || lines[0] != expected {
						t.Fatalf("lines %v, want %s", lines, expected)
					}
				})
			}
		}
	}
}

func stringBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestConnectionProbeDisagreementWindow(t *testing.T) {
	for _, probeFirst := range []bool{false, true} {
		for _, gap := range []time.Duration{10*time.Minute - time.Nanosecond, 10 * time.Minute, 10*time.Minute + time.Nanosecond} {
			t.Run(stringBool(probeFirst)+"/"+gap.String(), func(t *testing.T) {
				now := time.Unix(1000, 0)
				tt, g := evidenceTunnel(&now)
				probe := func() { tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1}) }
				conn := func() { tt.recordConnection(g, "stall", "direct") }
				if probeFirst {
					probe()
				} else {
					conn()
				}
				now = now.Add(gap)
				if probeFirst {
					conn()
				} else {
					probe()
				}
				want := 0
				if gap < disagreementWindow {
					want = 1
				}
				if got := len(disagreementLines(tt)); got != want {
					t.Fatalf("got %d, want %d", got, want)
				}
			})
		}
	}
}

func TestConnectionProbeDisagreementRateLimit(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	probe := func(g *group) { tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1}) }
	tt.recordConnection(g, "stall", "direct")
	probe(g)
	now = now.Add(disagreementWindow - time.Nanosecond)
	tt.recordConnection(g, "refused", "route-a")
	probe(g)
	if len(disagreementLines(tt)) != 1 {
		t.Fatal(disagreementLines(tt))
	}
	second := newGroup("second.example.org", kindRelay, RouteDirect)
	tt.recordConnection(second, "stall", "direct")
	probe(second)
	if len(disagreementLines(tt)) != 2 {
		t.Fatal("limit crossed groups")
	}
	now = now.Add(time.Nanosecond)
	probe(g)
	if len(disagreementLines(tt)) != 3 {
		t.Fatal("limit did not expire", disagreementLines(tt))
	}
}

func TestConnectionEvidenceJSON(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	before, err := json.Marshal(tt.Status(0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "last_conn") || strings.Contains(string(before), `"other":`) {
		t.Fatal(string(before))
	}
	tt.connectionFeedback(g, RouteDirect, "1", false)
	snapshot := tt.Status(0)
	tt.connectionFeedback(g, RouteDirect, "2", true, false)
	if snapshot.Groups[g.name].LastConnResult.Outcome != "ok" {
		t.Fatal("status aliases live evidence")
	}
	tt.recordConnection(tt.other, "refused", "route-a")
	data, err := json.Marshal(tt.Status(0))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Groups map[string]struct {
			LastConn       time.Time        `json:"last_conn"`
			LastConnResult connectionResult `json:"last_conn_result"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Groups[g.name]
	if !got.LastConn.Equal(now) || got.LastConnResult != (connectionResult{Outcome: "later", Route: "direct"}) {
		t.Fatal(string(data))
	}
	if decoded.Groups["other"].LastConnResult.Route != "route-a" {
		t.Fatal("other evidence missing", string(data))
	}
}

func TestRealConnectionFeedbackEvidence(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		for _, outcome := range []string{"ok", "stall", "later"} {
			t.Run(stringBool(pooled)+"/"+outcome, func(t *testing.T) {
				now := time.Unix(1000, 0)
				tt, g := evidenceTunnel(&now)
				route := RouteDirect
				if pooled {
					tt.client = &routePool{}
					route = RouteTunnel
				}
				tt.connectionFeedbackVia(g, route, "1", "route-a", outcome != "ok", outcome != "later")
				if g.lastConnResult != (connectionResult{Outcome: outcome, Route: "route-a"}) || !g.lastConn.Equal(now) {
					t.Fatal(g.lastConnResult, g.lastConn)
				}
			})
		}
	}
}

func TestConnectionEvidenceFirstReply(t *testing.T) {
	var count atomic.Int32
	tt := newTestTunnel(t, ModeOff, echoDialer("D:", &count), nil)
	c, code := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:443")
	defer c.Close()
	if code != repSucceeded || roundTrip(t, c) != "D:hi" {
		t.Fatal("connection failed")
	}
	got := tt.Status(0).Groups["relay.example.org"]
	if got.LastConn == nil || got.LastConnResult == nil || got.LastConnResult.Outcome != "ok" || got.LastConnResult.Route != "direct" {
		t.Fatalf("%+v", got)
	}
}

func TestConnectionEvidenceDialFallback(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	tt.mode = ModeAuto
	tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1})
	var attempts []string
	tt.ways[RouteDirect] = way{dial: func(context.Context, string, string) (net.Conn, error) {
		attempts = append(attempts, "direct")
		return nil, errors.New("unavailable")
	}, timeout: time.Second}
	tt.ways[RouteTunnel] = way{dial: func(context.Context, string, string) (net.Conn, error) {
		attempts = append(attempts, "tunnel")
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}, timeout: time.Second}
	c, route, err := tt.dial(g, "relay.example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if route != RouteTunnel || !reflect.DeepEqual(attempts, []string{"direct", "tunnel"}) || g.route != RouteDirect {
		t.Fatal(route, attempts, g.route)
	}
	if g.lastConnResult != (connectionResult{Outcome: "refused", Route: "direct"}) || len(disagreementLines(tt)) != 1 {
		t.Fatal(g.lastConnResult, disagreementLines(tt))
	}
}

func TestConnectionEvidencePoolRefusal(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	p := &routePool{ids: []string{"route-a", "route-b"}, kinds: []string{"http", "http"}, ipv6: []bool{false, false}, log: tt.log}
	p.clients = []soarClient{
		&poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused }},
		&poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { a, b := net.Pipe(); b.Close(); return a, nil }},
	}
	tt.client = p
	g.route = RouteTunnel
	tt.ways[RouteTunnel] = way{dial: p.DialContext, timeout: time.Second}
	c, r, err := tt.dial(g, "relay.example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if g.lastConnResult != (connectionResult{}) {
		t.Fatal("exit policy refusal recorded:", g.lastConnResult)
	}
	tt.connectionFeedbackVia(g, r, "1", connectionRoute(c, r), false)
	if g.lastConnResult != (connectionResult{Outcome: "ok", Route: "route-b"}) {
		t.Fatal(g.lastConnResult)
	}
	if p.stalls[""][0] != nil || !p.failedUntil[0].IsZero() {
		t.Fatal("refusal changed route health")
	}
}

func TestConnectionEvidencePreservesHoldAndRecovery(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	tt.mode = ModeAuto
	tt.ways[RouteTunnel].dial = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	tt.connectionFeedback(g, RouteDirect, "1", true)
	tt.connectionFeedback(g, RouteDirect, "2", true)
	hold := g.stalls[RouteDirect].until
	if g.route != RouteTunnel {
		t.Fatal(g.route)
	}
	tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true})
	if g.route != RouteTunnel || g.stalls[RouteDirect].until != hold || g.lastResult.RelayOK != 1 {
		t.Fatal("probe changed hold")
	}
	if len(disagreementLines(tt)) != 1 {
		t.Fatal("raw probe success was hidden", disagreementLines(tt))
	}
	tt.connectionFeedback(g, RouteDirect, "3", false)
	for n := 1; n <= recoveredAfter; n++ {
		tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1, NetworkOK: true})
		want := RouteTunnel
		if n == recoveredAfter {
			want = RouteDirect
		}
		if g.route != want {
			t.Fatalf("probe %d route %s, want %s", n, g.route, want)
		}
	}
}

func TestConnectionEvidenceIgnoresBackgroundDial(t *testing.T) {
	calls := 0
	ctx := context.WithValue(context.Background(), connectionObserverKey{}, func(string, error, time.Duration) { calls++ })
	p := &routePool{ids: []string{"route-a"}, ipv6: []bool{false}, clients: []soarClient{
		&poolFixture{dial: func(context.Context, string, string) (net.Conn, error) { return nil, errDestinationRefused }},
	}}
	if _, err := p.probeDialContext(ctx, "tcp", "relay.example.org:443"); err == nil {
		t.Fatal("probe unexpectedly succeeded")
	}
	if calls != 0 {
		t.Fatal("probe recorded connection evidence")
	}
	p.refused = nil
	if _, err := p.DialContext(ctx, "tcp", "relay.example.org:443"); err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
	if calls != 1 {
		t.Fatalf("recorded %d real outcomes, want 1", calls)
	}
}

func TestFailedDialIsATimeoutOnlyForATimeout(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		outcome string
		secs    int
	}{
		{"deadline", fmt.Errorf("dial: %w", context.DeadlineExceeded), "timeout", 8},
		{"network timeout", &net.OpError{Op: "dial", Err: timeoutError{}}, "timeout", 8},
		{"refused", errors.New("connection refused"), "refused", 0},
	}
	for _, c := range cases {
		now := time.Unix(1000, 0)
		tt, g := evidenceTunnel(&now)
		tt.recordFailedDial(g, "direct", c.err, 8*time.Second)
		got := g.lastConnResult
		if got.Outcome != c.outcome || got.AfterSecs != c.secs || got.Route != "direct" {
			t.Errorf("%s: result %+v, want %s after %d s", c.name, got, c.outcome, c.secs)
		}
	}
}

func TestTimeoutDisagreementNamesTheWait(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	tt.applyProbe(g, probeResult{Relays: 1, RelayOK: 1})
	tt.recordFailedDial(g, "direct", context.DeadlineExceeded, 8*time.Second)
	want := "disagree: " + g.historyUnit() + " direct probe=ok conn=timeout after 8s via direct"
	if lines := disagreementLines(tt); len(lines) != 1 || lines[0] != want {
		t.Fatalf("lines %v, want %s", lines, want)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestPoolObserverGetsTheFailureAndItsWait(t *testing.T) {
	type seen struct {
		via    string
		err    error
		waited time.Duration
	}
	var got []seen
	ctx := context.WithValue(context.Background(), connectionObserverKey{}, func(via string, err error, waited time.Duration) {
		got = append(got, seen{via, err, waited})
	})
	p := &routePool{ids: []string{"route-a"}, ipv6: []bool{false}, clients: []soarClient{
		&poolFixture{dial: func(context.Context, string, string) (net.Conn, error) {
			time.Sleep(30 * time.Millisecond)
			return nil, context.DeadlineExceeded
		}},
	}}
	if _, err := p.DialContext(ctx, "tcp", "relay.example.org:443"); err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
	if len(got) != 1 || !errors.Is(got[0].err, context.DeadlineExceeded) || got[0].waited < 25*time.Millisecond {
		t.Fatalf("observer calls = %+v", got)
	}
	g := &group{}
	tun := &Tunnel{}
	tun.recordFailedDial(g, got[0].via, got[0].err, 8*time.Second)
	if g.lastConnResult.Outcome != "timeout" || g.lastConnResult.AfterSecs != 8 {
		t.Fatalf("a pool timeout was recorded as %+v", g.lastConnResult)
	}
}
