package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/FOGProject/fog-agent/internal/netboot"
	"github.com/FOGProject/fog-agent/internal/reboot"
)

func taskReason(id int) reboot.Reason {
	return reboot.Reason{Source: reboot.SourceTask, Detail: "imaging", TaskID: id}
}

func namedReason(src string) reboot.Reason {
	return reboot.Reason{Source: src, Detail: src + " wants a reboot"}
}

// The whole point of design 0013: a task reboot that cannot reach the task
// must not happen. Without this the machine reboots, comes back on its own
// disk with the task still queued, and the next poll asks again -- a reboot
// loop whose only symptom is a machine nobody can use.
func TestATaskRebootIsWithheldWhenNothingCanBeArmed(t *testing.T) {
	pending := []reboot.Reason{taskReason(42)}
	act, withheld, refusal := planReboot(pending, netboot.ErrNoOption)

	if len(act) != 0 {
		t.Errorf("acting on %d reason(s); the machine would reboot to nowhere", len(act))
	}
	if len(withheld) != 1 || withheld[0].TaskID != 42 {
		t.Errorf("withheld = %+v, want the task reason held so it stays pending", withheld)
	}
	if refusal == "" {
		t.Error("no refusal text, so the task would sit still with nothing said about why")
	}
}

// A BIOS machine must still reboot for a task.
//
// This is the regression the withholding policy nearly shipped. Most of
// FOG's fleet has always reached FOS on a BIOS machine whose firmware boot
// order is network-first -- no arming, nothing for the agent to do. There
// are no EFI variables there, so Find() returns ErrUnsupported, and holding
// the task back on that would stop every one of those machines imaging.
//
// The distinction: ErrNoOption says the firmware HAS a boot manager holding
// no network entry, which is evidence. ErrUnsupported says only that there
// is no boot manager to ask, which is not.
func TestABiosMachineStillRebootsForATask(t *testing.T) {
	pending := []reboot.Reason{taskReason(42), namedReason("hostname")}
	act, withheld, refusal := planReboot(pending, netboot.ErrUnsupported)

	if len(act) != 2 {
		t.Errorf("acting on %d reason(s), want both: a BIOS machine images "+
			"today by its own boot order and must keep doing so", len(act))
	}
	if withheld != nil {
		t.Errorf("withheld %+v, want nothing held back on a machine with no "+
			"boot manager to consult", withheld)
	}
	if refusal != "" {
		t.Errorf("refusal %q, want none: the task is not being refused", refusal)
	}
}

// The same for a failure that is a failed measurement rather than a
// finding. "I could not look" is not "there is nothing there".
func TestAnUnreadableFirmwareDoesNotHoldTheTask(t *testing.T) {
	pending := []reboot.Reason{taskReason(9)}
	act, withheld, refusal := planReboot(pending, errors.New("efivarfs is read-only"))

	if len(act) != 1 || withheld != nil || refusal != "" {
		t.Errorf("act=%d withheld=%+v refusal=%q, want the reboot to proceed unarmed",
			len(act), withheld, refusal)
	}
}

// A machine must not be held hostage by a task it cannot serve: everything
// that is not a task still gets its reboot, because those want the machine
// back in its own OS, which a plain reboot delivers.
func TestOnlyTheTaskReasonIsWithheld(t *testing.T) {
	pending := []reboot.Reason{taskReason(42), namedReason("hostname"), namedReason("software")}
	act, withheld, refusal := planReboot(pending, netboot.ErrNoOption)

	if len(withheld) != 1 || withheld[0].Source != reboot.SourceTask {
		t.Errorf("withheld = %+v, want only the task reason", withheld)
	}
	if len(act) != 2 {
		t.Fatalf("acting on %d reason(s), want the two non-task ones", len(act))
	}
	for _, r := range act {
		if r.Source == reboot.SourceTask {
			t.Error("a task reason survived into the reasons being acted on")
		}
	}
	if refusal == "" {
		t.Error("the withheld task was not explained")
	}
}

func TestNothingIsWithheldWhenTheFirmwareCanBeArmed(t *testing.T) {
	pending := []reboot.Reason{taskReason(7), namedReason("hostname")}
	act, withheld, refusal := planReboot(pending, nil)

	if len(act) != 2 {
		t.Errorf("acting on %d reason(s), want both", len(act))
	}
	if withheld != nil || refusal != "" {
		t.Errorf("withheld %+v refusal %q, want neither when arming succeeded", withheld, refusal)
	}
}

// A firmware failure with no task pending is not a reason to hold anything
// back: nothing needed the network in the first place.
func TestAFirmwareFailureWithNoTaskHoldsNothing(t *testing.T) {
	pending := []reboot.Reason{namedReason("hostname")}
	act, withheld, refusal := planReboot(pending, netboot.ErrNoOption)

	if len(act) != 1 || withheld != nil || refusal != "" {
		t.Errorf("act=%d withheld=%+v refusal=%q, want the hostname reboot to proceed untouched",
			len(act), withheld, refusal)
	}
}

// The two failures need different sentences, and now they also mean
// different things: one refuses the task, the other explains why the reboot
// is going ahead unarmed. Reading alike would be worse than before, because
// an admin would not be able to tell a held task from a running one.
func TestARefusalAndANoteAreNotInterchangeable(t *testing.T) {
	noEntry := netbootRefusal(netboot.ErrNoOption)
	noUEFI := netbootNote(netboot.ErrUnsupported)

	if noUEFI == noEntry {
		t.Fatal("both firmware failures produce the same sentence")
	}
	if !strings.Contains(noEntry, "firmware setup") {
		t.Errorf("%q does not tell the admin what to change", noEntry)
	}
	// The note must not read as a refusal: the task IS proceeding.
	for _, word := range []string{"cannot be armed", "withheld", "will not"} {
		if strings.Contains(noUEFI, word) {
			t.Errorf("the BIOS note reads like a refusal (%q): %q", word, noUEFI)
		}
	}
	if !strings.Contains(noUEFI, "as it always has") {
		t.Errorf("the BIOS note does not say the machine behaves as before: %q", noUEFI)
	}

	other := netbootRefusal(errors.New("efivarfs is read-only"))
	if !strings.Contains(other, "efivarfs is read-only") {
		t.Errorf("an unexpected failure lost its cause: %q", other)
	}
	if !strings.Contains(netbootNote(errors.New("boom")), "boom") {
		t.Error("the note lost the cause of an unexpected failure")
	}
}

// The policy itself, stated once so it cannot drift by accident.
func TestOnlyAMissingBootEntryHoldsATask(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
		why  string
	}{
		{netboot.ErrNoOption, true, "a boot manager with no network entry is evidence"},
		{netboot.ErrUnsupported, false, "no boot manager to ask is not evidence"},
		{errors.New("efivarfs is read-only"), false, "a failed read is not evidence"},
	} {
		if got := withholdsTask(c.err); got != c.want {
			t.Errorf("withholdsTask(%v) = %v, want %v: %s", c.err, got, c.want, c.why)
		}
	}
}

// findNetboot is the seam the coordinator uses; if it stops being
// swappable, none of the above is reachable from a test any more.
func TestFindNetbootIsSwappable(t *testing.T) {
	old := findNetboot
	t.Cleanup(func() { findNetboot = old })

	want := netboot.Option{Number: 3, Description: "Onboard NIC(IPV4)", IPv4: true}
	findNetboot = func() (netboot.Option, error) { return want, nil }
	got, err := findNetboot()
	if err != nil || got != want {
		t.Fatalf("findNetboot() = %v, %v", got, err)
	}
}
