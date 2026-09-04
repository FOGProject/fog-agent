package wake

import "syscall"

// setBroadcast permits writes to a broadcast address on this socket.
//
// Same call as everywhere else; Windows just spells a descriptor
// differently.
func setBroadcast(fd uintptr) error {
	return syscall.SetsockoptInt(
		syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1,
	)
}
