package main

import (
	"testing"
	"time"
)

// The policy has to actually restart, and it has to forget failures fast
// enough that routine updates keep getting the quick one.
//
// Self-update on Windows works by swapping the binary and exiting non-zero
// so the service manager starts the new one. Two ways that goes wrong, both
// seen for real on 2026-09-07: an empty policy leaves the machine with a new
// binary and nothing running, and a reset window of a day means the third
// update in an afternoon sits stopped for five minutes after a swap that
// worked perfectly.
func TestTheRestartPolicyComesBackAndForgetsInTime(t *testing.T) {
	if len(restartDelays) == 0 {
		t.Fatal("no restart delays: a self-update would leave the service stopped")
	}
	if restartDelays[0] > 30*time.Second {
		t.Errorf("the first restart waits %v; an ordinary update should come "+
			"back promptly, not after a visible outage", restartDelays[0])
	}
	for i := 1; i < len(restartDelays); i++ {
		if restartDelays[i] <= restartDelays[i-1] {
			t.Errorf("delay %d (%v) does not back off after %v: a binary that "+
				"crashes on start would spin as fast as Windows allows",
				i, restartDelays[i], restartDelays[i-1])
		}
	}
	if restartResetWindow <= 0 {
		t.Fatal("a zero reset window never clears the failure count, so the " +
			"fast restarts run out and stay out")
	}
	if restartResetWindow > time.Hour {
		t.Errorf("reset window is %v: longer than an hour and repeated updates "+
			"exhaust the fast restarts, so a healthy swap sits stopped for "+
			"minutes", restartResetWindow)
	}
}
