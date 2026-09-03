//go:build !windows

package main

import (
	"errors"
	"runtime"
)

// isService is only ever true under the Windows service control manager.
func isService() bool { return false }

func runService() error { return nil }

// cmdService is Windows only: on Linux and macOS the agent is run by
// systemd or launchd from the unit the package installs.
func cmdService([]string) error {
	return errors.New("the service command is for Windows; on " + runtime.GOOS + " run the agent from a service unit")
}
