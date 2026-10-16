package enroll

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows reports a failed connect with the Winsock code, which the
// syscall package's ECONNREFUSED and ETIMEDOUT (invented values on
// Windows) never equal, and which Errno.Timeout does not count.

func isRefused(e syscall.Errno) bool { return e == windows.WSAECONNREFUSED }

func isNoRoute(e syscall.Errno) bool {
	return e == windows.WSAENETUNREACH || e == windows.WSAEHOSTUNREACH
}

func isTimedOut(e syscall.Errno) bool { return e == windows.WSAETIMEDOUT }
