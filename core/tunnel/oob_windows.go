package tunnel

import (
	"net"

	"golang.org/x/sys/windows"
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
		buf := windows.WSABuf{Len: uint32(len(data)), Buf: &data[0]}
		var sent uint32
		serr = windows.WSASend(windows.Handle(fd), &buf, 1, &sent, windows.MSG_OOB, nil, nil)
		return true
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	return len(b), nil
}
