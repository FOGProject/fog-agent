package hostname

import (
	"errors"
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
