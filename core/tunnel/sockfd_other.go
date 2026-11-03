//go:build !windows

package tunnel

// sockfd turns a raw connection's fd into what syscall's socket options take.
func sockfd(fd uintptr) int { return int(fd) }
