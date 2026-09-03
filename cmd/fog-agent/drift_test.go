package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/provider/software"
)

// A missing backend is looked for every poll: once it appears the set is
// due even though the drift interval has not elapsed.
func TestDriftDueWhenBlockedBackendAppears(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows path is ProgramData, not PATH")
	}
	policy := &software.Policy{DriftInterval: 3600, Entries: []software.Entry{{Package: "x"}}}
	now := time.Now()
	st := &enroll.State{}
	st.Config.SoftwareChecked = now.Add(-time.Minute)
	t.Setenv("PATH", t.TempDir())
	if driftDue(st, policy, now) {
		t.Fatal("due with nothing blocked and the interval not elapsed")
	}
	st.Config.SoftwareBlocked = true
	if driftDue(st, policy, now) {
		t.Fatal("due while the backend is still missing")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "choco"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if !driftDue(st, policy, now) {
		t.Fatal("not due once the backend appeared")
	}
}
