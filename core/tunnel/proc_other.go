//go:build !windows

package tunnel

import "os/exec"

func hideConsole(*exec.Cmd) {}
