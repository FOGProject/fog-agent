//go:build !windows

package wake

import "syscall"

// setBroadcast permits writes to a broadcast address on this socket.
func setBroadcast(fd uintptr) error {
	return syscall.SetsockoptInt(
		int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1,
	)
}
