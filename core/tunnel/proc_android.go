package tunnel

import (
	"context"
	"os"
	"syscall"
)

// startHelper runs a helper until ctx ends; the channel closes when it exits.
//
// It avoids os/exec, which first probes pidfd system calls. Before Android 12
// seccomp answers those with SIGSYS, and a Go shared library has no handler
// for that signal, so the probe would kill the app.
func startHelper(ctx context.Context, path string, args, env []string, dir string) (<-chan struct{}, error) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer null.Close()
	fd := null.Fd()
	pid, err := syscall.ForkExec(path, append([]string{path}, args...), &syscall.ProcAttr{
		Dir:   dir,
		Env:   env,
		Files: []uintptr{fd, fd, fd},
	})
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var status syscall.WaitStatus
		for {
			if _, err := syscall.Wait4(pid, &status, 0, nil); err != syscall.EINTR {
				return
			}
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			syscall.Kill(pid, syscall.SIGKILL)
		case <-done:
		}
	}()
	return done, nil
}
