package main

import (
	"context"
	"time"
)

// setupPollInterval is how often the agent re-reads the Windows setup state
// while it waits. Setup passes take minutes, so a short interval only costs
// a registry read.
var setupPollInterval = 30 * time.Second

// imageStateComplete is the ImageState value Windows writes when setup has
// finished: after OOBE, before SetupComplete.cmd runs and before the first
// logon.
const imageStateComplete = "IMAGE_STATE_COMPLETE"

// setupFinished reports whether Windows setup has finished. A missing value
// means the machine never went through Sysprep setup, so nothing is running
// and the agent may act. Any other value (UNDEPLOYABLE, a RESEAL_TO_OOBE or
// RESEAL_TO_AUDIT state) means setup or audit mode is still in progress.
func setupFinished(state string, present bool) bool {
	return !present || state == imageStateComplete
}

// waitForWindowsSetup blocks until Windows setup has finished. With nobody
// logged in, the agent reboots at once for a rename or a domain join, and
// Windows runs the specialize and OOBE passes with nobody logged in. A reboot
// there stops setup with "Windows could not complete the installation". So
// the agent does nothing at all, not even enroll, until setup is done.
//
// read returns the ImageState value and whether it exists. A read error is
// logged and treated as finished: an unreadable key must not stop the agent
// forever on a machine that is long past setup.
func waitForWindowsSetup(ctx context.Context, read func() (string, bool, error), out *sayer) error {
	for {
		state, present, err := read()
		if err != nil {
			out.say("could not read the Windows setup state (" + err.Error() +
				"); continuing as if setup has finished")
			return nil
		}
		if setupFinished(state, present) {
			return nil
		}
		out.say("Windows setup has not finished (ImageState " + state +
			"); the agent waits for it before it enrolls or acts")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(setupPollInterval):
		}
	}
}
