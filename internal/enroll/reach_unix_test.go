//go:build !windows

package enroll

import "syscall"

// What a failed connect carries on Linux and macOS.
const (
	connectSyscall = "connect"
	refusedErrno   = syscall.ECONNREFUSED
	noRouteErrno   = syscall.EHOSTUNREACH
	timedOutErrno  = syscall.ETIMEDOUT
)
