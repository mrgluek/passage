package tunnel

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// The strategy is shared: a failed relay that it cannot reach must not tear
// it down while another failed relay uses it.
func TestSmartStrategySurvivesAnotherFailedRelay(t *testing.T) {
	var n atomic.Int32
	tt := newTestTunnel(t, ModeAuto, echoDialer("D:", &n), echoDialer("T:", &n))
	tt.testGroup("a.example.org:993")
	tt.testGroup("b.example.org:993")
	tt.probeTLS = func(_ context.Context, hp, _ string) error {
		if hp == testCanaries[0] {
			return nil
		}
		return errors.New("refused")
	}
	tt.strategies = []strategy{{"frag", echoDialer("S:", &n)}}
	var aWorks atomic.Bool
	aWorks.Store(true)
	tt.smartProbe = func(_ context.Context, _ dialFunc, hp, _ string) error {
		if hp == "a.example.org:993" && aWorks.Load() {
			return nil
		}
		return errors.New("refused")
	}
	for i := range 4 {
		for g, r := range tt.probe() {
			if g.kind == kindRelay && r.SmartOK != (g.name == "a.example.org") {
				t.Fatalf("pass %d: %s smart ok = %v", i, g.name, r.SmartOK)
			}
		}
		tt.mu.Lock()
		kept := tt.strategy != nil && tt.ways[RouteSmart].dial != nil
		tt.mu.Unlock()
		if !kept {
			t.Fatalf("pass %d: strategy dropped while a relay still uses it", i)
		}
	}
	aWorks.Store(false)
	tt.probe()
	tt.mu.Lock()
	gone := tt.strategy == nil && tt.ways[RouteSmart].dial == nil
	tt.mu.Unlock()
	if !gone {
		t.Fatal("strategy kept although no relay can use it")
	}
}
