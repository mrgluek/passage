package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func telegramProbeResult(total, answered, dcOK int) probeResult {
	res := probeResult{Relays: total, RelayOK: answered, NetworkOK: answered > 0}
	if total > 0 {
		res.TelegramDCOK = &dcOK
	}
	return res
}

func TestTelegramProbeVerdict(t *testing.T) {
	partial := telegramProbeResult(9, 1, 1)
	for _, tc := range []struct {
		name      string
		probe     probeResult
		outcomes  []string
		relay     bool
		wantState string
		wantRoute Route
	}{
		{"lone answer with failures", partial, []string{"refused", "timeout"}, false, "blocked", RouteTunnel},
		{"lone answer without connections", partial, nil, false, "blocked", RouteDirect},
		{"real reply clears failures", partial, []string{"stall", "timeout", "ok"}, false, "direct", RouteDirect},
		{"several learned answers", telegramProbeResult(9, 3, 0), nil, false, "direct", RouteDirect},
		{"one configured and one learned answer", telegramProbeResult(9, 2, 1), nil, false, "direct", RouteDirect},
		{"two main DCs with failures", telegramProbeResult(9, 3, 2), []string{"refused", "timeout"}, false, "direct", RouteDirect},
		{"no probe targets", telegramProbeResult(0, 0, 0), []string{"refused", "timeout"}, false, "unknown", RouteDirect},
		{"relay still needs one answer", probeResult{Relays: 1, RelayOK: 1, NetworkOK: true}, []string{"refused", "timeout"}, true, "direct", RouteDirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			tt := memoryTunnel(Config{}, &now)
			g := tt.newGroup("telegram", kindTelegram, RouteDirect)
			if tc.relay {
				g = tt.newGroup("relay.example.org", kindRelay, RouteDirect)
			}
			for _, outcome := range tc.outcomes {
				tt.recordConnection(g, outcome, "direct")
				now = now.Add(time.Second)
			}
			tt.applyProbe(g, tc.probe)
			if g.state != tc.wantState || g.route != tc.wantRoute {
				t.Fatalf("state %q, route %s, want %q, %s", g.state, g.route, tc.wantState, tc.wantRoute)
			}
			if tc.name == "real reply clears failures" {
				tt.recordConnection(g, "refused", "direct")
				now = now.Add(time.Second)
				tt.applyProbe(g, tc.probe)
				if g.route != RouteDirect || g.failStreak != 1 || g.repeatedDirectFailures(now) {
					t.Fatal("a failure after a reply was treated as a repeated failure")
				}
				if lines := disagreementLines(tt); len(lines) != 1 ||
					lines[0] != "disagree: telegram direct probe=failed conn=ok via direct" {
					t.Fatalf("disagreement history: %v", lines)
				}
			}
		})
	}
}

func TestTelegramPartialProbeDoesNotFlap(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, useStalls := range []bool{false, true} {
		tt := memoryTunnel(Config{}, &now)
		g := tt.newGroup("telegram", kindTelegram, RouteDirect)
		partial := telegramProbeResult(9, 1, 1)
		if useStalls {
			tt.connectionFeedback(g, RouteDirect, "destination-a", true)
			now = now.Add(time.Second)
			tt.connectionFeedback(g, RouteDirect, "destination-b", true)
		} else {
			tt.recordFailedDial(g, "direct", errors.New("refused"), 0)
			now = now.Add(time.Second)
			tt.recordFailedDial(g, "direct", context.DeadlineExceeded, directDialTimeout)
		}
		tt.applyProbe(g, partial)
		if g.route != RouteTunnel || g.state != "blocked" {
			t.Fatalf("stalls=%v: route %s, state %s", useStalls, g.route, g.state)
		}
		hold := g.stalls[RouteDirect]
		if hold == nil || hold.until.Sub(now) != 2*time.Minute {
			t.Fatalf("stalls=%v: missing two-minute direct hold: %+v", useStalls, hold)
		}

		for _, gap := range []time.Duration{36, 51, 70, 36, 70} {
			now = now.Add(gap * time.Second)
			tt.recordConnection(g, "ok", "route-a")
			tt.recordConnection(g, "timeout", "direct")
			g.tunnelFails = 1 // The old verdict could switch back in a single round.
			tt.applyProbe(g, partial)
			if g.route != RouteTunnel || g.state != "blocked" || g.okStreak != 0 {
				t.Fatalf("stalls=%v, gap=%ds: route %s, state %s, recovery streak %d", useStalls, gap, g.route, g.state, g.okStreak)
			}
		}
	}
}

func TestTelegramPartialProbeAllowsRealTrialRecovery(t *testing.T) {
	now := time.Unix(1000, 0)
	tt := memoryTunnel(Config{}, &now)
	g := tt.newGroup("telegram", kindTelegram, RouteDirect)
	partial := telegramProbeResult(9, 1, 1)
	tt.recordConnection(g, "stall", "direct")
	tt.recordConnection(g, "refused", "direct")
	tt.applyProbe(g, partial)
	usable := func(Route) bool { return true }
	now = now.Add(2*time.Minute - time.Second)
	tt.tickGroupLocked(g, now)
	if trial := tt.startTrialLocked(g, usable, RouteTunnel); trial != nil {
		t.Fatal("trial started before hold expired")
	}
	now = now.Add(time.Second)
	tt.tickGroupLocked(g, now)
	trial := tt.startTrialLocked(g, usable, RouteTunnel)
	if trial == nil || trial.route != RouteDirect || g.route != RouteTunnel {
		t.Fatalf("partial probe did not leave the group on the tunnel with a direct trial: %+v", trial)
	}
	tt.finishTrial(trial, false)
	tt.applyProbe(g, partial)
	if g.route != RouteDirect || g.state != "direct" || g.repeatedDirectFailures(now) {
		t.Fatalf("trial reply did not restore direct: route %s, state %s", g.route, g.state)
	}
}

func TestTelegramDirectFailureEvidenceExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	tt := memoryTunnel(Config{}, &now)
	g := tt.newGroup("telegram", kindTelegram, RouteDirect)
	tt.recordFailedDial(g, "direct", context.Canceled, time.Second)
	tt.recordFailedDial(g, "direct", context.DeadlineExceeded, directDialTimeout)
	if g.repeatedDirectFailures(now) {
		t.Fatal("canceled dial counted as a direct failure")
	}
	now = now.Add(disagreementWindow - time.Second)
	tt.recordFailedDial(g, "direct", errors.New("refused"), 0)
	if !g.repeatedDirectFailures(now) {
		t.Fatal("two recent direct failures did not corroborate each other")
	}
	now = now.Add(time.Second)
	if g.repeatedDirectFailures(now) {
		t.Fatal("the older failure did not expire")
	}
	tt.recordConnection(g, "later", "direct")
	if g.repeatedDirectFailures(now) {
		t.Fatal("later stall counted as a first-reply failure")
	}
	tt.recordFailedDial(g, "direct", context.DeadlineExceeded, directDialTimeout)
	if !g.repeatedDirectFailures(now) {
		t.Fatal("two recent direct failures did not corroborate each other")
	}
	now = now.Add(disagreementWindow)
	if g.repeatedDirectFailures(now) {
		t.Fatal("failure streak did not expire")
	}
}

func TestTelegramPartialProbeConfirmsTunnelBeforeEvidenceSwitch(t *testing.T) {
	now := time.Unix(1000, 0)
	tt := memoryTunnel(Config{}, &now)
	tt.ctx = context.Background()
	tt.established = func() bool { return true }
	tt.probeMTTunnel = func(context.Context, string) error { return errors.New("refused") }
	g := tt.newGroup("telegram", kindTelegram, RouteDirect)
	tt.applyProbe(g, telegramProbeResult(9, 3, 2))
	tt.recordConnection(g, "timeout", "direct")
	tt.recordConnection(g, "refused", "direct")
	partial := telegramProbeResult(9, 1, 1)
	partial.hosts = telegramDCs
	checked := tt.confirmViaTunnel(g, partial)
	if !checked.TunnelChecked || checked.TunnelOK {
		t.Fatalf("missing failed tunnel confirmation: %+v", checked)
	}
	tt.applyProbe(g, checked)
	if g.state != stateRelaysDown || g.route != RouteDirect {
		t.Fatalf("failed tunnel check changed route: state %s, route %s", g.state, g.route)
	}
}

func TestTelegramMainDCRecoveryUsesExistingStreak(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tunnelHealthy := range []bool{true, false} {
		tt := memoryTunnel(Config{}, &now)
		tt.ways[RouteDirect] = way{dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("unused")
		}}
		g := tt.newGroup("telegram", kindTelegram, RouteTunnel)
		wantRound := recoveredAfter
		if !tunnelHealthy {
			wantRound = 1
			g.tunnelFails = 1
		}
		tt.applyProbe(g, telegramProbeResult(9, 1, 1))
		for round := 1; round <= wantRound; round++ {
			now = now.Add(30 * time.Second)
			tt.applyProbe(g, telegramProbeResult(9, 3, 2))
			if g.state != "direct" || (g.route == RouteDirect) != (round == wantRound) {
				t.Fatalf("healthy=%v, round=%d: route %s, state %s", tunnelHealthy, round, g.route, g.state)
			}
		}
	}
}

func TestTelegramProbeCountsMainAndLearnedTargets(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, _ := evidenceTunnel(&now)
	g := tt.telegram
	tt.ipv6Route = func() bool { return false }
	tt.learn(g, "192.0.2.1:443")
	tt.learn(g, telegramDCs[0])
	for _, tc := range []struct {
		mainAnswers int
		wantDirect  bool
	}{
		{0, false},
		{1, true},
		{2, true},
	} {
		tt.probeMT = func(_ context.Context, addr string) error {
			if addr == "192.0.2.1:443" {
				return nil
			}
			for _, dc := range telegramDCs[:tc.mainAnswers] {
				if addr == dc {
					return nil
				}
			}
			return errors.New("refused")
		}
		res := tt.probe()[g]
		if res.Relays != len(telegramDCs)+1 || res.RelayOK != 1+tc.mainAnswers ||
			res.TelegramDCOK == nil || *res.TelegramDCOK != tc.mainAnswers || res.directOK() != tc.wantDirect ||
			!res.NetworkOK || res.CanaryChecked {
			t.Fatalf("%d working main DCs: %+v", tc.mainAnswers, res)
		}
	}
	data, err := json.Marshal(telegramProbeResult(9, 1, 0))
	if err != nil || !strings.Contains(string(data), `"telegram_dc_ok":0`) {
		t.Fatalf("zero main DCs must appear in Telegram probe results: %s, %v", data, err)
	}
	data, err = json.Marshal(probeResult{Relays: 1, RelayOK: 1})
	if err != nil || strings.Contains(string(data), "telegram_dc_ok") {
		t.Fatalf("unrelated probe JSON changed: %s, %v", data, err)
	}
}
