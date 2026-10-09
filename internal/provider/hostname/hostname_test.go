package hostname

import (
	"errors"
	"strings"
	"testing"

	"github.com/FOGProject/fog-agent/internal/provider"
)

func fake(t *testing.T, have string, reboot bool, setErr error) *string {
	t.Helper()
	var got string
	oldCurrent, oldSet := current, set
	current = func() (string, error) { return have, nil }
	set = func(name string) (bool, error) { got = name; return reboot, setErr }
	t.Cleanup(func() { current, set = oldCurrent, oldSet })
	return &got
}

// TestEnsureLeavesAMatchingNameAlone pins the idempotence that makes this a
// convergence provider rather than a task: a name that already matches,
// in any case, is never set.
func TestEnsureLeavesAMatchingNameAlone(t *testing.T) {
	got := fake(t, "LAB-01", false, nil)
	r := Ensure(Desired{Name: "lab-01"})
	if r.Status != provider.StatusUnchanged || *got != "" {
		t.Fatalf("matching name: want unchanged and no set, got %+v set=%q", r, *got)
	}
}

// TestEnsureSetsAndReportsHowItEnded pins the three outcomes of a real
// rename: applied, pending a reboot, or failed with the reason.
func TestEnsureSetsAndReportsHowItEnded(t *testing.T) {
	got := fake(t, "old", false, nil)
	if r := Ensure(Desired{Name: "new"}); r.Status != provider.StatusApplied || *got != "new" || r.Detail != "old -> new" {
		t.Fatalf("applied: got %+v set=%q", r, *got)
	}
	fake(t, "old", true, nil)
	if r := Ensure(Desired{Name: "new"}); r.Status != provider.StatusPendingReboot {
		t.Fatalf("reboot needed: got %+v", r)
	}
	fake(t, "old", false, errors.New("nope"))
	if r := Ensure(Desired{Name: "new"}); r.Status != provider.StatusFailed || r.Detail != "old -> new: nope" {
		t.Fatalf("failed: got %+v", r)
	}
	got = fake(t, "old", false, nil)
	if r := Ensure(Desired{Name: "  "}); r.Status != provider.StatusFailed || *got != "" {
		t.Fatalf("empty desired name must fail without setting: got %+v set=%q", r, *got)
	}
}

// fakeJoined makes the machine a domain member with the given name pending.
func fakeJoined(t *testing.T, pendingName string) {
	t.Helper()
	oldJoined, oldPending := joined, pending
	joined = func() bool { return true }
	pending = func() string { return pendingName }
	t.Cleanup(func() { joined, pending = oldJoined, oldPending })
}

// TestEnsureNeverRenamesADomainMemberAlone pins design 0017 section 3.3: a
// joined machine renamed locally comes back under a name its computer
// object does not carry. The directory capability renames both; until it
// has, this provider fails with the reason and sets nothing.
func TestEnsureNeverRenamesADomainMemberAlone(t *testing.T) {
	got := fake(t, "old", true, nil)
	fakeJoined(t, "")
	r := Ensure(Desired{Name: "new"})
	if r.Status != provider.StatusFailed || *got != "" {
		t.Fatalf("joined, nothing pending: want failed and no set, got %+v set=%q", r, *got)
	}
}

// TestEnsureReportsADomainRenameAsPendingReboot pins the other half: once
// the directory has renamed the machine, the new name is pending and the
// reboot is this provider's to ask for, under the host's enforce flag.
func TestEnsureReportsADomainRenameAsPendingReboot(t *testing.T) {
	got := fake(t, "old", true, nil)
	fakeJoined(t, "NEW")
	r := Ensure(Desired{Name: "new"})
	if r.Status != provider.StatusPendingReboot || *got != "" {
		t.Fatalf("joined, rename pending: want pending_reboot and no set, got %+v set=%q", r, *got)
	}
}

// TestEnsureReportsAHeldRenameAsPending pins the field report of
// 2026-10-09: a rename held by the server's join cooldown is pending, with
// the time it may go ahead, not failed with no reason.
func TestEnsureReportsAHeldRenameAsPending(t *testing.T) {
	got := fake(t, "old", true, nil)
	fakeJoined(t, "")
	r := Ensure(Desired{Name: "new", WaitUntil: "2026-10-09T19:20:00Z"})
	if r.Status != provider.StatusPending || *got != "" || !strings.Contains(r.Detail, "runs after") {
		t.Fatalf("held: want pending with the time and no set, got %+v set=%q", r, *got)
	}
}
