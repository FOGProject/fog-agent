package main

import "time"

// The service restart policy, kept here rather than in the Windows file so
// it can be tested on any machine. Only Windows consumes it (see
// recoveryActions in service_windows.go), but the invariants it has to
// satisfy are arithmetic, and a test that can only be compiled and never
// run is not a gate.
var (
	// restartDelays is one delay per consecutive failure. Windows keeps
	// using the last entry once the list runs out, so the final value is
	// the steady-state wait for a service that keeps failing.
	restartDelays = []time.Duration{
		10 * time.Second,
		time.Minute,
		5 * time.Minute,
	}

	// restartResetWindow is how long the service must stay up before the
	// failure count goes back to the start of that list.
	//
	// An hour, not a day. A self-update is a DELIBERATE failure -- the
	// agent exits non-zero so that it gets restarted -- so every update
	// spends one slot, sharing a budget with real crashes, and Windows
	// offers no way to tell the two apart. With a day-long window a third
	// update inside the same day waits five minutes to come back;
	// observed 2026-09-07, when several updates in one afternoon left the
	// agent down for the full 300s after a perfectly healthy swap.
	//
	// An hour is long enough that a binary crash-looping on start still
	// backs off exactly as it did, and short enough that ordinary updates
	// always get the ten-second restart.
	restartResetWindow = time.Hour
)
