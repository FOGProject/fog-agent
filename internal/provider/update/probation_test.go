package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/provider"
)

// arm writes a probation record the way a real update would, and returns
// the state directory.
func armed(t *testing.T, deadline time.Time) string {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(Probation{From: "0.2.0", To: "0.3.0", Sequence: 7, Deadline: deadline})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, probationFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestClearProbationStopsTheRevert is the success half of probation, and
// it is the half whose failure is worse. A revert that does not happen
// leaves one host on a bad build; a clear that does not happen reverts
// every host in the fleet fifteen minutes after every GOOD update, which
// is a rollout that undoes itself and a fleet that flaps forever.
func TestClearProbationStopsTheRevert(t *testing.T) {
	// A deadline already in the past: if the record survives the clear,
	// the very next loop iteration reverts.
	dir := armed(t, time.Now().Add(-time.Minute))

	p, err := LoadProbation(dir)
	if err != nil || p == nil {
		t.Fatalf("setup: LoadProbation = %v, %v; want a record", p, err)
	}
	if !p.Due(time.Now()) {
		t.Fatal("setup: the record should be due, or this proves nothing")
	}

	if err := ClearProbation(dir); err != nil {
		t.Fatalf("ClearProbation: %v", err)
	}

	// Assert through the same call the run loop makes, not by stat-ing
	// the file: what matters is that checkProbation finds nothing to act
	// on, and that is LoadProbation returning nil.
	p, err = LoadProbation(dir)
	if err != nil {
		t.Fatalf("LoadProbation after clear: %v", err)
	}
	if p != nil {
		t.Fatalf("probation survived the clear (%+v); every good update would revert itself", p)
	}
}

// TestClearProbationKeepsPrev pins the thing the comment on
// ClearProbation used to get wrong. .prev is not scratch space -- it is
// the only source `fog-agent update-revert` restores from, and the
// failure it covers (a build that polls happily and behaves badly) is
// found by a person hours after probation has already passed.
func TestClearProbationKeepsPrev(t *testing.T) {
	dir := armed(t, time.Now().Add(time.Hour))
	prev := filepath.Join(dir, "fog-agent"+prevSuffix)
	if err := os.WriteFile(prev, []byte("the binary this replaced"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ClearProbation(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prev); err != nil {
		t.Fatalf("ClearProbation removed %s: %v -- a hands-on revert now has nothing to restore", prevSuffix, err)
	}
}

// TestCorruptProbationDoesNotRevert: reverting on a parse error would
// turn a truncated write into a downgrade. The caller must get an error,
// not a zero-valued record whose deadline is the zero time and therefore
// always due.
func TestCorruptProbationDoesNotRevert(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, probationFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProbation(dir)
	if err == nil {
		t.Fatal("a corrupt record parsed cleanly")
	}
	if p != nil {
		t.Fatalf("got a record %+v from a corrupt file; its zero deadline is always due, so this reverts", p)
	}
}

// TestRevertLeavesAReportForTheRestoredBinary pins the only way the
// server ever learns a revert happened. The process that reverts is being
// stopped, and the usual reason it is being stopped is that it could not
// reach the server -- so if the fact is not written down here, nothing
// reports it, and a fleet-wide bad release looks from the server exactly
// like a rollout that has not finished.
func TestRevertLeavesAReportForTheRestoredBinary(t *testing.T) {
	l := newLab(t)
	cfg := l.cfg()
	if res, _ := Run(context.Background(), Desired{Version: "0.4.2"}, cfg); res.Status != provider.StatusApplied {
		t.Fatalf("setup: %+v", res)
	}
	if _, err := Revert(cfg, "no successful poll before 2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("revert: %v", err)
	}

	r, err := LoadReverted(l.state)
	if err != nil {
		t.Fatalf("LoadReverted: %v", err)
	}
	if r == nil {
		t.Fatal("a revert left no report; the server can never record agent.update.reverted")
	}
	// From and To are the transition that was UNDONE, in the same
	// direction the probation record states it, so the server can render
	// "0.4.2 -> 0.1.1" without knowing which way round a revert runs.
	if r.To != "0.4.2" || r.From != "0.1.1" {
		t.Errorf("report does not name the transition undone: %+v", r)
	}
	if r.Reason == "" {
		t.Error("report carries no reason; refused and reverted then look identical on the server")
	}
	if r.At.IsZero() {
		t.Error("report carries no timestamp")
	}
}

// TestRevertReportIsKeptUntilItIsCleared is the delivery guarantee. The
// report is cleared by the code that has just had it ACCEPTED by the
// server, never by the code that read it -- a poll that fails to send
// must find it again next time.
func TestRevertReportIsKeptUntilItIsCleared(t *testing.T) {
	l := newLab(t)
	cfg := l.cfg()
	if res, _ := Run(context.Background(), Desired{Version: "0.4.2"}, cfg); res.Status != provider.StatusApplied {
		t.Fatalf("setup: %+v", res)
	}
	if _, err := Revert(cfg, "test"); err != nil {
		t.Fatalf("revert: %v", err)
	}

	// Reading it does not consume it: this is the failed-send case.
	if r, _ := LoadReverted(l.state); r == nil {
		t.Fatal("setup: no report to begin with")
	}
	if r, _ := LoadReverted(l.state); r == nil {
		t.Fatal("reading the report consumed it; a failed send would lose it forever")
	}

	if err := ClearReverted(l.state); err != nil {
		t.Fatalf("ClearReverted: %v", err)
	}
	if r, _ := LoadReverted(l.state); r != nil {
		t.Fatalf("the report survived the clear (%+v); the server would be told twice", r)
	}
	// Idempotent: a second clear is what happens when a report is taken
	// and the agent restarts before it finishes.
	if err := ClearReverted(l.state); err != nil {
		t.Errorf("clearing an already-cleared report must be a no-op, got %v", err)
	}
}

// TestCorruptRevertReportIsDiscarded keeps a truncated write from wedging
// the agent on one unreadable file forever. The report is a courtesy;
// losing one is much cheaper than never being able to send another.
func TestCorruptRevertReportIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, revertedFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadReverted(dir)
	if err != nil {
		t.Fatalf("a corrupt report must not be an error, got %v", err)
	}
	if r != nil {
		t.Fatalf("a corrupt report must not be reported as a revert: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, revertedFile)); !os.IsNotExist(err) {
		t.Error("the corrupt report was left on disk; every poll would retry it forever")
	}
}
