package main

import (
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
