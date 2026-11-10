package tunnel

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func firstProbeTunnel(t *testing.T) *Tunnel {
	t.Helper()
	tt := &Tunnel{
		mode: ModeAuto, log: newEventLog(30), kick: make(chan struct{}, 1),
		relays:   map[string]*group{},
		telegram: newGroup("telegram", kindTelegram, RouteDirect),
		other:    newGroup("other", kindOther, RouteDirect),
		probeTLS: func(context.Context, string, string) error { return nil },
	}
	tt.ctx, tt.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() { tt.cancel(); tt.wg.Wait() })
	return tt
}

func TestFirstRelayUseKicksProbeOnce(t *testing.T) {
	tt := firstProbeTunnel(t)
	g := tt.testGroup("relay.example.org:993")
	select {
	case <-tt.kick:
	default:
		t.Fatal("first relay use did not kick a probe")
	}
	if again := tt.testGroup("RELAY.example.org:443"); again != g {
		t.Fatal("second use created another group")
	}
	select {
	case <-tt.kick:
		t.Fatal("second relay use kicked another probe")
	default:
	}
	if tt.urgent.Load() {
		t.Fatal("first contact bypassed the minimum probe gap")
	}
}

func TestFirstRelayBurstCoalescesProbe(t *testing.T) {
	tt := firstProbeTunnel(t)
	for i := range maxRelayGroups {
		tt.testGroup(fmt.Sprintf("r%d.example.org:443", i))
	}
	if got := len(tt.kick); got != 1 {
		t.Fatalf("queued kicks = %d, want 1", got)
	}
	if tt.urgent.Load() {
		t.Fatal("relay burst bypassed the minimum probe gap")
	}
}

func TestFirstRelayUseMonitorUpdatesStatus(t *testing.T) {
	tt := firstProbeTunnel(t)
	if got := tt.nextProbeIn(); got != time.Hour {
		t.Fatalf("unused monitor interval = %v, want one hour", got)
	}
	tt.wg.Add(1)
	go tt.monitor()
	tt.testGroup("relay.example.org:993")
	waitFirstProbeStatus(t, tt, "relay.example.org")
	s := tt.Status(0)
	if s.LastProbe.IsZero() || s.Groups["relay.example.org"].State != "direct" {
		t.Fatalf("first probe status = %+v", s.Groups["relay.example.org"])
	}
}

func TestFirstRelayBurstRespectsMinProbeGap(t *testing.T) {
	tt := firstProbeTunnel(t)
	const remaining = 100 * time.Millisecond
	now := time.Now()
	tt.now = func() time.Time { return now }
	tt.lastProbe = now.Add(-minProbeGap + remaining)
	var calls atomic.Int32
	tt.probeTLS = func(context.Context, string, string) error {
		if elapsed := time.Since(now); elapsed < remaining {
			t.Errorf("probe started after %v, want at least %v", elapsed, remaining)
		}
		calls.Add(1)
		return nil
	}
	for i := range maxRelayGroups {
		tt.testGroup(fmt.Sprintf("r%d.example.org:443", i))
	}
	tt.wg.Add(1)
	go tt.monitor()
	for i := range maxRelayGroups {
		waitFirstProbeStatus(t, tt, fmt.Sprintf("r%d.example.org", i))
	}
	if got := calls.Load(); got != int32(maxRelayGroups) {
		t.Fatalf("probe calls = %d, want one per group in one round", got)
	}
	if len(tt.kick) != 0 || tt.urgent.Load() {
		t.Fatal("relay burst left another kick or bypassed the minimum gap")
	}
}

func TestFixedProbeTargetsKeepInitialSchedule(t *testing.T) {
	tt := firstProbeTunnel(t)
	tt.cfg.ProbeTargets = []string{"relay.example.org:993"}
	tt.useProbeTargets()
	if len(tt.kick) != 0 || tt.nextProbeIn() != time.Minute {
		t.Fatal("fixed probe target's initial schedule changed")
	}
	tt.testGroup("relay.example.org:993")
	if len(tt.kick) != 0 {
		t.Fatal("fixed probe target's first traffic changed its schedule")
	}
}

func waitFirstProbeStatus(t *testing.T, tt *Tunnel, host string) {
	t.Helper()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for tt.Status(0).Groups[host].LastProbe.IsZero() {
		select {
		case <-timeout.C:
			t.Fatalf("monitor did not update last_probe for %s", host)
		case <-tick.C:
		}
	}
}
