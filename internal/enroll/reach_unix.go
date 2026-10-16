//go:build !windows

package enroll

import "syscall"

func isRefused(e syscall.Errno) bool { return e == syscall.ECONNREFUSED }

func isNoRoute(e syscall.Errno) bool {
	return e == syscall.ENETUNREACH || e == syscall.EHOSTUNREACH
}

func isTimedOut(e syscall.Errno) bool { return e == syscall.ETIMEDOUT }
