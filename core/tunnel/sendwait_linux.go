package tunnel

import (
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// waitSent waits until the socket has no unsent bytes, at most 50 ms.
func waitSent(c *net.TCPConn) {
	raw, err := c.SyscallConn()
	if err != nil {
		return
	}
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		left := uint32(0)
		if raw.Control(func(fd uintptr) {
			if info, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO); err == nil {
				left = info.Notsent_bytes
			}
		}) != nil || left == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
