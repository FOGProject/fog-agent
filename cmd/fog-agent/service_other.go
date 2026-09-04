//go:build !windows

package main

import (
	"errors"
	"os"
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

// The state directory keeps the permissions its package gave it, and
// there is no event log source or service log file to set up: the unit
// captures stderr.
func restrictStateDir(string) error     { return nil }
func postSetup() error                  { return nil }
func setupLog(string) (*os.File, error) { return nil, nil }
