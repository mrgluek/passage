package tunnel

import (
	"errors"
	"net"
	"syscall"
	"time"
)

func listenProxy(address string, timeout time.Duration) (net.Listener, bool, error) {
	deadline := time.Now().Add(timeout)
	retried := false
	for {
		ln, err := net.Listen("tcp", address)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) || !time.Now().Before(deadline) {
			return ln, retried, err
		}
		retried = true
		time.Sleep(min(100*time.Millisecond, time.Until(deadline)))
	}
}
