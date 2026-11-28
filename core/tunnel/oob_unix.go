//go:build !windows

package tunnel

import (
	"net"
	"syscall"
)

// sendOOB sends b followed by one urgent byte.
func sendOOB(c *net.TCPConn, b []byte) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	data := append(append([]byte{}, b...), 'a')
	var serr error
	if err := raw.Write(func(fd uintptr) bool {
		serr = syscall.Sendto(int(fd), data, syscall.MSG_OOB, nil)
		return serr != syscall.EAGAIN
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	return len(b), nil
}
