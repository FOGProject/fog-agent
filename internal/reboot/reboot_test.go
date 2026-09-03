package reboot

import (
	"strings"
	"testing"
	"time"
)

var policy = Policy{Grace: 60}

// TestNothingPendingNeverReboots: the coordinator with no reason is inert,
// whoever is or is not logged in.
func TestNothingPendingNeverReboots(t *testing.T) {
	for _, n := range []int{0, 1} {
		if d := Decide(nil, n, policy); d.Reboot || d.Why != "" {
			t.Fatalf("no reasons, %d logged in: got %+v", n, d)
		}
	}
}

// TestEmptyMachineRebootsAtOnce: with nobody logged in, an unforced reason
// is enough and there is no countdown to wait out.
func TestEmptyMachineRebootsAtOnce(t *testing.T) {
	d := Decide([]Reason{{Source: "hostname", Detail: "a -> b"}}, 0, policy)
	if !d.Reboot || d.Delay != 0 {
		t.Fatalf("empty machine: got %+v", d)
	}
}

// TestUnforcedWaitsForUsers pins the legacy meaning of an unticked
// "enforce" flag and FOG_TASK_FORCE_REBOOT=0: the machine is not taken
// from under someone.
func TestUnforcedWaitsForUsers(t *testing.T) {
	d := Decide([]Reason{{Source: SourceTask, Detail: "task 7"}}, 2, policy)
	if d.Reboot || !strings.Contains(d.Why, "2 logged-in user") {
		t.Fatalf("unforced with users: got %+v", d)
	}
}

// TestForcedRebootsWithGrace: a forced reason reboots past a logged-in
// user, but with the policy's warning, not instantly.
func TestForcedRebootsWithGrace(t *testing.T) {
	reasons := []Reason{
		{Source: "hostname", Detail: "a -> b"},
		{Source: SourceTask, Detail: "task 7", Force: true},
	}
	d := Decide(reasons, 1, policy)
	if !d.Reboot || d.Delay != 60*time.Second {
		t.Fatalf("forced with a user: got %+v", d)
	}
	// One reboot satisfies every reason, so all of them are named.
	if !strings.Contains(d.Why, "hostname: a -> b") || !strings.Contains(d.Why, "task: task 7") {
		t.Fatalf("why does not name both reasons: %q", d.Why)
	}
}

// TestMergeReplacesSameSource: a source asking twice holds one request,
// and withdrawing it leaves the others alone.
func TestMergeReplacesSameSource(t *testing.T) {
	r := Merge(nil, Reason{Source: SourceTask, Detail: "task 7"})
	r = Merge(r, Reason{Source: "hostname", Detail: "a -> b"})
	r = Merge(r, Reason{Source: SourceTask, Detail: "task 8"})
	if len(r) != 2 {
		t.Fatalf("want 2 reasons, got %+v", r)
	}
	for _, x := range r {
		if x.Since.IsZero() {
			t.Fatalf("Since not stamped: %+v", x)
		}
		if x.Source == SourceTask && x.Detail != "task 8" {
			t.Fatalf("task reason not replaced: %+v", x)
		}
	}
	r = Drop(r, SourceTask)
	if len(r) != 1 || r[0].Source != "hostname" {
		t.Fatalf("drop task: got %+v", r)
	}
}
