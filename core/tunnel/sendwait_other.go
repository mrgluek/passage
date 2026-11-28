//go:build !linux

package tunnel

import (
	"net"
	"time"
)

// waitSent gives the socket a short time to send: this system tells no
// unsent count to an unprivileged program in the same simple way.
func waitSent(*net.TCPConn) { time.Sleep(5 * time.Millisecond) }
