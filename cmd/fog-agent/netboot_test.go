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

// A machine must not be held hostage by a task it cannot serve: everything
// that is not a task still gets its reboot, because those want the machine
// back in its own OS, which a plain reboot delivers.
func TestOnlyTheTaskReasonIsWithheld(t *testing.T) {
	pending := []reboot.Reason{taskReason(42), namedReason("hostname"), namedReason("software")}
	act, withheld, refusal := planReboot(pending, netboot.ErrUnsupported)

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

// The two failures need different fixes -- a BIOS machine needs its boot
// order changed, a UEFI machine needs PXE switched on in setup -- so the
// sentences an admin reads must not be interchangeable.
func TestTheTwoRefusalsReadDifferently(t *testing.T) {
	noUEFI := netbootRefusal(netboot.ErrUnsupported)
	noEntry := netbootRefusal(netboot.ErrNoOption)

	if noUEFI == noEntry {
		t.Fatal("both firmware failures produce the same sentence")
	}
	for _, c := range []struct {
		got, want string
	}{
		{noUEFI, "boot from the network"},
		{noEntry, "firmware setup"},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Errorf("%q does not tell the admin what to do (looking for %q)", c.got, c.want)
		}
	}

	other := netbootRefusal(errors.New("efivarfs is read-only"))
	if !strings.Contains(other, "efivarfs is read-only") {
		t.Errorf("an unexpected failure lost its cause: %q", other)
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
