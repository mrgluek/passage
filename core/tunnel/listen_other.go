//go:build !windows

package tunnel

import "syscall"

var errAddrInUse error = syscall.EADDRINUSE
