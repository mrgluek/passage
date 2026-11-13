package tunnel

import "syscall"

// WSAEADDRINUSE; syscall.EADDRINUSE is a different value on Windows.
var errAddrInUse error = syscall.Errno(10048)
