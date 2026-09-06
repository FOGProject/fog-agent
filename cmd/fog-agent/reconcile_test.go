package main

import (
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
