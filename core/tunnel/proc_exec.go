//go:build !android

package tunnel

import (
	"context"
	"io"
	"os/exec"
	"time"
)

// startHelper runs a helper until ctx ends; the channel closes when it exits.
func startHelper(ctx context.Context, path string, args, env []string, dir string) (<-chan struct{}, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.WaitDelay = helperWaitDelay // a child holding the pipes must not hang Wait
	hideConsole(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	return done, nil
}

// helperWaitDelay bounds how long Wait lingers on the helper's pipes after it is killed.
const helperWaitDelay = 2 * time.Second
