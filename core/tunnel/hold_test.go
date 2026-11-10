package tunnel

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHoldTransitions(t *testing.T) {
	now := time.Unix(1000, 0)
	h := &hold{}
	log := newEventLog(100, func() time.Time { return now })
	for _, tc := range []struct {
		name  string
		event func()
		state holdState
		step  int
		count int64
	}{
		{"open", func() { h.tick(now, log, "relay-1 direct") }, Open, 0, 0},
		{"later counts only", func() { h.onStall(now, log, "relay-1 direct", false, "a") }, Open, 0, 1},
		{"first point", func() { h.onStall(now, log, "relay-1 direct", true, "a") }, Suspect, 0, 2},
		{"duplicate", func() { h.onStall(now, log, "relay-1 direct", true, "a") }, Suspect, 0, 3},
		{"points expire", func() { now = now.Add(121 * time.Second); h.tick(now, log, "relay-1 direct") }, Open, 0, 3},
		{"first again", func() { h.onStall(now, log, "relay-1 direct", true, "b") }, Suspect, 0, 4},
		{"reply clears", func() { h.onReply(log, "relay-1 direct") }, Open, 0, 4},
		{"new point", func() { h.onStall(now, log, "relay-1 direct", true, "c") }, Suspect, 0, 5},
		{"corroborated", func() { h.onStall(now, log, "relay-1 direct", true, "d") }, Held, 1, 6},
		{"held counts only", func() { h.onStall(now, log, "relay-1 direct", true, "e") }, Held, 1, 7},
		{"expiry", func() { now = now.Add(2 * time.Minute); h.tick(now, log, "relay-1 direct") }, Trial, 1, 7},
		{"trial reply", func() { h.onReply(log, "relay-1 direct") }, Open, 0, 7},
		{"pinned first", func() { h.onStall(now, log, "relay-1 direct", true, "f", true) }, Suspect, 0, 8},
		{"pinned pair", func() { h.onStall(now, log, "relay-1 direct", true, "g", true) }, Suspect, 0, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.event()
			if h.state != tc.state || h.step != tc.step || h.count != tc.count {
				t.Fatalf("hold = %+v", h)
			}
		})
	}
	t.Run("trial keeps ladder", func(t *testing.T) {
		h := &hold{}
		for step, delay := range []time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
			h.onStall(now, log, "relay-1 direct", true, "a")
			h.onStall(now, log, "relay-1 direct", true, "b")
			if h.state != Held || h.step != step+1 || h.until != now.Add(delay) {
				t.Fatalf("hold = %+v, want step %d for %v", h, step+1, delay)
			}
			now = h.until.Add(time.Nanosecond)
			h.tick(now, log, "relay-1 direct")
			if h.state != Trial || !h.until.IsZero() {
				t.Fatalf("expired hold = %+v", h)
			}
		}
		h.onReply(log, "relay-1 direct")
		if h.state != Open || h.step != 0 {
			t.Fatalf("reply = %+v", h)
		}
	})

}

func TestHoldHistoryLines(t *testing.T) {
	for _, tc := range []struct {
		event historyTransition
		want  string
	}{
		{historyTransition{prefix: "stalled", unit: "relay-1 direct", reason: "first reply", delay: 2 * time.Minute, count: 2}, "stalled: relay-1 direct (first reply), paused for 2 min, count=2"},
		{historyTransition{prefix: "stalled", unit: "route-a (exit=a)", reason: "first reply, 2 destinations", delay: 10 * time.Minute, count: 7}, "stalled: route-a (exit=a) (first reply, 2 destinations), paused for 10 min, count=7"},
		{historyTransition{prefix: "unblocked", unit: "telegram direct", reason: "cooldown ended"}, "resumed: telegram direct (cooldown ended)"},
		{historyTransition{prefix: "unblocked", unit: "other smart", reason: "first reply"}, "resumed: other smart (first reply)"},
		{historyTransition{prefix: "disagree", unit: "relay-1", probe: "ok", outcome: "stall", route: "direct"}, "disagree: relay-1 probe=ok conn=stall via direct"},
	} {
		if got := historyLine(tc.event); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestTwoGroupsTwoStalledLines(t *testing.T) {
	now := time.Unix(1000, 0)
	tt := &Tunnel{log: newEventLog(100), now: func() time.Time { return now }, mode: ModeAuto}
	var firstOrdinal uint64
	for i, name := range []string{"a.example.org", "b.example.org"} {
		g := newGroup(name, kindRelay, RouteDirect)
		if i == 0 {
			firstOrdinal = g.ordinal
		} else if g.ordinal != firstOrdinal+1 {
			t.Fatal("relay ordinals are not consecutive")
		}
		tt.connectionFeedback(g, RouteDirect, "a", true)
		tt.connectionFeedback(g, RouteDirect, "b", true)
	}
	var lines []string
	for _, e := range tt.log.last(100) {
		if strings.HasPrefix(e.Msg, "stalled:") {
			if !e.Time.Equal(now) {
				t.Fatalf("timestamp = %v, want %v", e.Time, now)
			}
			lines = append(lines, e.Msg)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	for i, line := range lines {
		if line != fmt.Sprintf("stalled: relay-%d direct (first reply), paused for 2 min, count=2", firstOrdinal+uint64(i)) {
			t.Fatal(line)
		}
	}
}

func TestStatusHasNoSideEffects(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	tt.connectionFeedback(g, RouteDirect, "a", true)
	tt.connectionFeedback(g, RouteDirect, "b", true)
	p := &routePool{log: tt.log, now: tt.timeNow, clients: []soarClient{&poolFixture{}}, ids: []string{"route-a"}, kinds: []string{"http"}, ipv6: []bool{false}}
	tt.client = p
	poolStall(t, p, 0, "")
	poolStall(t, p, 0, "")
	before := *g.stalls[RouteDirect]
	before.points = append(before.points[:0:0], before.points...)
	poolBefore := *p.stalls[""][0]
	poolBefore.points = append(poolBefore.points[:0:0], poolBefore.points...)
	events := tt.log.last(100)
	for range 3 {
		tt.Status(100)
		now = now.Add(10 * time.Minute)
	}
	if !reflect.DeepEqual(before, *g.stalls[RouteDirect]) || !reflect.DeepEqual(poolBefore, *p.stalls[""][0]) || !reflect.DeepEqual(events, tt.log.last(100)) {
		t.Fatal("Status mutated evidence or history")
	}
	g.okStreak = recoveredAfter
	tt.mode = ModeAuto
	tt.tick(now)
	if g.stalls[RouteDirect].state != Trial || p.stalls[""][0].state != Trial {
		t.Fatal("tick did not expire holds")
	}
	for _, e := range tt.log.last(100)[len(events):] {
		if e.Time != before.until.UTC() {
			t.Fatalf("expiry time = %v, want %v", e.Time, before.until)
		}
	}
}

func TestDisagreementUsesGroupKind(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	g.lastProbe = now
	g.lastResult = probeResult{Relays: 1, RelayOK: 1}
	tt.recordConnection(g, "stall", "direct")
	lines := disagreementLines(tt)
	if len(lines) != 1 || lines[0] != "disagree: "+g.historyUnit()+" probe=ok conn=stall via direct" {
		t.Fatal(lines)
	}
}

func TestInjectedClock(t *testing.T) {
	now := time.Unix(1000, 0)
	tt, g := evidenceTunnel(&now)
	tt.log = newEventLog(100, tt.timeNow)
	tt.ways[RouteTunnel].dial = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	created := tt.newGroup("example.org", kindRelay, RouteDirect)
	if created.routeSince != now {
		t.Fatal(created.routeSince)
	}
	tt.setRouteLocked(g, RouteTunnel, "test")
	if g.routeSince != now {
		t.Fatal(g.routeSince)
	}
	tt.check()
	if tt.lastProbe != now {
		t.Fatal(tt.lastProbe)
	}
	if tt.log.last(1)[0].Time != now.UTC() {
		t.Fatal(tt.log.last(1))
	}
}

func TestStepOnlyReplyDoesNotLogResume(t *testing.T) {
	now := time.Unix(1000, 0)
	log := newEventLog(10, func() time.Time { return now })
	h := restoreStep(savedStep{Step: 2, At: now}, true)
	h.onReply(log, "relay-1 direct")
	if h.step != 0 || len(log.last(10)) != 0 {
		t.Fatalf("hold=%+v events=%v", h, log.last(10))
	}
}

func TestStallLogPrunesExpiredEntries(t *testing.T) {
	now := time.Unix(1000, 0)
	log := newEventLog(10)
	old, recent := &hold{}, &hold{}
	log.stalled(now, old, "relay-1 direct", "first reply", 2*time.Minute, 2)
	log.stalled(now.Add(30*time.Second), recent, "relay-2 direct", "first reply", 2*time.Minute, 2)
	if len(log.stallLogged) != 1 || log.stallLogged[recent].IsZero() {
		t.Fatalf("entries=%v", log.stallLogged)
	}
}
