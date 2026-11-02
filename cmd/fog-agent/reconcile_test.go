package main

import (
	"context"
	"strings"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// A revision applied by a build with a different capability set is
// unapplied to this one: an upgrade must converge what it inherited.
func TestNeedsReconcileAfterCapabilityUpgrade(t *testing.T) {
	cfg := enroll.Config{AppliedRevision: "abc", AppliedWith: supportedCapabilities}
	if needsReconcile(cfg, "abc") {
		t.Fatal("same revision, same build: nothing to do")
	}
	if !needsReconcile(cfg, "def") {
		t.Fatal("a new revision must reconcile")
	}
	cfg.AppliedWith = "hostname,taskreboot,software,snapin" // the pre-power build
	if !needsReconcile(cfg, "abc") {
		t.Fatal("a revision applied without power must be reconciled by a build that has it")
	}
	cfg.AppliedWith = "" // a config written before the field existed
	if !needsReconcile(cfg, "abc") {
		t.Fatal("an unstamped revision must be reconciled once")
	}
}

// The update capability is in supportedCapabilities, which matters more
// than it looks: an agent that inherited an applied revision from a build
// without this code must re-converge, or the first host to be told to
// update would sit on the old revision until something unrelated moved it.
// That exact defect cost the Windows lab ten minutes on the power build.
func TestUpdateIsPartOfTheCapabilitySetThatForcesAReconcile(t *testing.T) {
	if !has(splitCaps(supportedCapabilities), "update") {
		t.Fatal("update is missing from supportedCapabilities")
	}
	cfg := enroll.Config{
		AppliedRevision: "abc123",
		// What a 0.1.1 build stored: the same revision, applied by a
		// build that had no update provider.
		AppliedWith: "hostname,taskreboot,power,software,printers,directory,wake,snapin,autologout",
	}
	if !needsReconcile(cfg, "abc123") {
		t.Error("a build that has learned update must re-converge the revision it inherited")
	}
}

func splitCaps(s string) []string { return strings.Split(s, ",") }

// A held directory/hostname change was stripped from the desired state, so
// a clean reconcile of the rest must not mark the revision applied.
func TestReconcileHeldDoesNotMarkRevisionApplied(t *testing.T) {
	for _, tc := range []struct {
		name    string
		held    bool
		wantRev string
	}{
		{name: "not held applies the revision", wantRev: "new"},
		{name: "held keeps the old revision", held: true, wantRev: "old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := enroll.Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			st.Config.AppliedRevision, st.Config.AppliedWith = "old", "before"
			desired := &enroll.DesiredState{Revision: "new"}
			if err := reconcileHeld(context.Background(), st, &enroll.Client{}, desired, &sayer{}, tc.held); err != nil {
				t.Fatal(err)
			}
			if st.Config.AppliedRevision != tc.wantRev {
				t.Fatalf("AppliedRevision = %q, want %q", st.Config.AppliedRevision, tc.wantRev)
			}
			if tc.held && st.Config.AppliedWith != "before" {
				t.Fatalf("AppliedWith = %q, want it restored", st.Config.AppliedWith)
			}
			saved, err := enroll.Load(st.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Config.AppliedRevision != tc.wantRev {
				t.Fatalf("saved AppliedRevision = %q, want %q", saved.Config.AppliedRevision, tc.wantRev)
			}
		})
	}
}
