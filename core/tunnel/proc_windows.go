package tunnel

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps a console helper from opening a window when Passage
// runs as a GUI program.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
