package tunnel

import "syscall"

// sockfd turns a raw connection's fd into what syscall's socket options take.
func sockfd(fd uintptr) syscall.Handle { return syscall.Handle(fd) }
