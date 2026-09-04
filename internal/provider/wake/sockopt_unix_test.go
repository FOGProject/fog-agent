//go:build !windows

package wake

import (
	"context"
	"net"
	"syscall"
	"testing"
)

// The gate on SO_BROADCAST. Reading the option back is the only assertion
// that holds, because whether the kernel ENFORCES it is a property of the
// kernel: this box's Linux happily writes to 255.255.255.255 without it,
// and Windows and the BSDs refuse. Verified to fail by removing the
// setsockopt from setBroadcast.
func TestTheSocketReallyHasBroadcastPermission(t *testing.T) {
	sender, err := NewUDPSender(context.Background())
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	defer sender.Close()

	raw, err := sender.conn.(*net.UDPConn).SyscallConn()
	if err != nil {
		t.Fatalf("syscallconn: %v", err)
	}

	var got int
	var optErr error
	if err := raw.Control(func(fd uintptr) {
		got, optErr = syscall.GetsockoptInt(
			int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST,
		)
	}); err != nil {
		t.Fatalf("control: %v", err)
	}
	if optErr != nil {
		t.Fatalf("getsockopt: %v", optErr)
	}
	if got == 0 {
		t.Fatal("SO_BROADCAST is off; a broadcast send is refused wherever " +
			"the platform enforces it")
	}
}
