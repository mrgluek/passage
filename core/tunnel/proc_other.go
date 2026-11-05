//go:build !windows && !android

package tunnel

import "os/exec"

func hideConsole(*exec.Cmd) {}
