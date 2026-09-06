package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
