//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows/svc/mgr"
)

// The policy has to actually restart. Self-update on Windows works by
// swapping the binary and exiting non-zero so the service manager starts
// the new one; a policy with no restart in it leaves the machine with a
// new binary and nothing running until someone reboots. That is what an
// empty policy did on every MSI-installed agent (see ensureRecoveryActions).
func TestTheRecoveryPolicyActuallyRestarts(t *testing.T) {
	actions, reset := recoveryActions()
	if len(actions) == 0 {
		t.Fatal("no recovery actions: a self-update would leave the service stopped")
	}
	if actions[0].Type != mgr.ServiceRestart {
		t.Fatalf("the FIRST action must be a restart, got %v: anything else means "+
			"the service does not come back from the exit self-update relies on",
			actions[0].Type)
	}
	if reset == 0 {
		t.Error("a zero reset period never clears the failure count, so the " +
			"restarts run out and stay out")
	}
	// Backoff must not be all-at-once: a binary that crashes on start would
	// otherwise spin as fast as Windows allows.
	for i := 1; i < len(actions); i++ {
		if actions[i].Delay <= actions[i-1].Delay {
			t.Errorf("action %d does not back off (%v after %v)", i, actions[i].Delay, actions[i-1].Delay)
		}
	}
}
