//go:build !android && !windows

package tunnel

import (
	"context"
	"testing"
	"time"
)

// A child of the helper that keeps the pipes open must not keep the helper's
// channel from closing once the helper is killed.
func TestStartHelperDoneDespiteChildHoldingPipes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, err := startHelper(ctx, "/bin/sh", []string{"-c", "sleep 8 & wait"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(helperWaitDelay + 3*time.Second):
		t.Fatal("helper's done channel did not close")
	}
}
