package main

import (
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// The printers drift check: an assigned queue somebody deleted comes back
// without an admin having to touch the assignment to move the revision.
func TestPrintersDriftDueOnlyWhileManaged(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		cfg  enroll.Config
		want bool
	}{
		{"never converged, managed", enroll.Config{PrintersManaged: true}, true},
		{"interval elapsed", enroll.Config{
			PrintersManaged: true, PrintersChecked: now.Add(-2 * FactsInterval)}, true},
		{"just converged", enroll.Config{
			PrintersManaged: true, PrintersChecked: now.Add(-time.Minute)}, false},
		// Mode off. Nothing is due, ever: a host FOG does not manage
		// printers on must not have its spooler read on a timer.
		{"not managed", enroll.Config{
			PrintersChecked: now.Add(-2 * FactsInterval)}, false},
		{"never managed, never checked", enroll.Config{}, false},
	}
	for _, c := range cases {
		if got := printersDriftDue(c.cfg, now); got != c.want {
			t.Errorf("%s: printersDriftDue = %t, want %t", c.name, got, c.want)
		}
	}
}

// The poll asks the server for state when any subsystem is due, not only
// when software is: without this the printers drift check could never fire,
// because the state it needs would never be sent.
func TestDriftMayBeDueCoversPrintersOnAHostWithNoSoftwareSet(t *testing.T) {
	st := &enroll.State{}
	if driftMayBeDue(st) {
		t.Fatal("due on a fresh config with nothing managed")
	}
	st.Config.PrintersManaged = true
	st.Config.PrintersChecked = time.Now().Add(-2 * FactsInterval)
	if !driftMayBeDue(st) {
		t.Fatal("the state is never fetched, so the printers never re-converge")
	}
}

// A build that learns the printers capability must re-run the revision it
// inherited, or a host sits on its old state until something unrelated moves
// it -- the Windows lab upgrade that sat on an on-demand shutdown for ten
// minutes, arrived at from a different direction.
func TestSupportedCapabilitiesNamesPrinters(t *testing.T) {
	if !needsReconcile(enroll.Config{
		AppliedRevision: "abc", AppliedWith: "hostname,taskreboot,power,software,snapin",
	}, "abc") {
		t.Fatal("an upgrade that learned printers treated the revision as applied")
	}
}
