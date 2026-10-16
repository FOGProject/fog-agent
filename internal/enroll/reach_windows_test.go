package enroll

import "golang.org/x/sys/windows"

// What a failed connect carries on Windows: the Winsock code from
// connectex. timedOutErrno is the 10060 in the telliottwin11 log of
// 2026-10-16.
const (
	connectSyscall = "connectex"
	refusedErrno   = windows.WSAECONNREFUSED
	noRouteErrno   = windows.WSAEHOSTUNREACH
	timedOutErrno  = windows.WSAETIMEDOUT
)
