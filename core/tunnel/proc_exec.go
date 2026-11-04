//go:build !android

package tunnel

import (
	"context"
	"io"
	"os/exec"
)

// startHelper runs a helper until ctx ends; the channel closes when it exits.
func startHelper(ctx context.Context, path string, args, env []string, dir string) (<-chan struct{}, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	hideConsole(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	return done, nil
}
