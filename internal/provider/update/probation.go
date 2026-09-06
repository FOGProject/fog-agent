package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// The two files this package keeps, both beside the agent's config and
// neither inside it.
//
// Kept out of config.json deliberately. config.json holds the enrollment
// -- the host id, the server URL, what the reboot coordinator still owes
// -- and an update that corrupted it would turn "the new build is bad"
// into "this host needs an admin to approve it again", which is the worst
// thing in this design. A separate file means the update path never opens
// the file that must survive it.
const (
	probationFile = "update-probation.json"
	sequenceFile  = "update-sequence.json"
	prevSuffix    = ".prev"
	// badSuffix keeps the binary a revert rejected, for diagnosis.
	badSuffix = ".bad"
)

// Probation is what the outgoing binary leaves behind for the incoming
// one: what it replaced, and by when the replacement has to prove it
// works. "Works" is one successful authenticated poll -- not that it
// starts, which the service manager already checks, but that it can still
// talk to the server it is managed by.
type Probation struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Sequence int64     `json:"sequence"`
	Deadline time.Time `json:"deadline"`
}

// LoadSequence is the highest manifest sequence this agent has accepted.
// Missing is zero, which accepts anything: a fresh agent has no reason to
// distrust the first manifest it is shown, and the floor is about replays
// after that.
func LoadSequence(dir string) (int64, error) {
	b, err := os.ReadFile(filepath.Join(dir, sequenceFile))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var s struct {
		Sequence int64 `json:"sequence"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return 0, err
	}
	return s.Sequence, nil
}

// SaveSequence raises the floor. It never lowers it: a caller handed an
// older number has misread something, and quietly accepting it would undo
// the one protection against a replayed manifest.
func SaveSequence(dir string, n int64) error {
	if cur, err := LoadSequence(dir); err == nil && n < cur {
		return nil
	}
	b, _ := json.Marshal(struct {
		Sequence int64 `json:"sequence"`
	}{n})
	return writeFileSync(filepath.Join(dir, sequenceFile), b, 0o600)
}

// LoadProbation returns the record left by the binary that was replaced,
// or nil when this binary was not installed by an update.
func LoadProbation(dir string) (*Probation, error) {
	b, err := os.ReadFile(filepath.Join(dir, probationFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Probation
	if err := json.Unmarshal(b, &p); err != nil {
		// An unreadable probation record is not a reason to revert --
		// reverting on a parse error would make a corrupt file into a
		// downgrade -- but it must not be left to be re-read forever.
		return nil, fmt.Errorf("update: %s: %w", probationFile, err)
	}
	return &p, nil
}

// ClearProbation is what a new binary calls once it has proved itself.
// It also removes the kept previous binary: one spare is the design, and
// a second one accumulating per update is how a state directory fills a
// small disk.
func ClearProbation(dir string) error {
	return os.Remove(filepath.Join(dir, probationFile))
}

// Arm keeps everything a revert needs, and it does so before the swap so
// that the binary doing the arming is the one still known to work.
//
// It does not copy the binary: swap's first step renames the running one
// to .prev, which preserves it atomically and leaves exactly one source
// of truth for what the previous binary was. What Arm has to establish is
// that the record and the previous config are on the disk BEFORE the swap
// makes the new binary the one that runs -- because after that point, the
// code that would write them is the code being tested.
func Arm(cfg Config, p Probation) error {
	// A .bad binary from an earlier revert has served its purpose once
	// another update starts.
	_ = os.Remove(cfg.ExePath + badSuffix)
	if err := copyFile(filepath.Join(cfg.StateDir, "config.json"),
		filepath.Join(cfg.StateDir, "config.json"+prevSuffix), 0o600); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("keeping the current config: %w", err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := writeFileSync(filepath.Join(cfg.StateDir, probationFile), b, 0o600); err != nil {
		return err
	}
	// Read it back. The record is the only thing standing between a bad
	// update and a machine that needs hands, and "the write returned no
	// error" is not the same fact as "the record is on the disk".
	got, err := LoadProbation(cfg.StateDir)
	if err != nil || got == nil || got.To != p.To {
		return fmt.Errorf("the probation record did not survive being written")
	}
	return nil
}

// Revert puts the previous binary and config back. Called when the
// probation deadline passes without the new binary having managed a
// successful poll, and by `fog-agent update-revert`, which is what the
// service manager runs after the new binary has failed to start enough
// times to stop being a transient.
func Revert(cfg Config) (*Probation, error) {
	p, err := LoadProbation(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("update: nothing to revert to")
	}
	prev := cfg.ExePath + prevSuffix
	if _, err := os.Stat(prev); err != nil {
		return p, fmt.Errorf("update: the previous binary is gone: %w", err)
	}
	// The config goes back first. A new binary that wrote a state file an
	// old one cannot read is exactly the case a revert has to survive, and
	// restoring the binary first would leave a window where the old binary
	// is running against the new binary's state.
	prevCfg := filepath.Join(cfg.StateDir, "config.json"+prevSuffix)
	if _, err := os.Stat(prevCfg); err == nil {
		if err := copyFile(prevCfg, filepath.Join(cfg.StateDir, "config.json"), 0o600); err != nil {
			return p, fmt.Errorf("update: restoring the config: %w", err)
		}
	}
	// Not swap(): its source would be its own destination, and its first
	// rename would overwrite the binary being restored with the one being
	// replaced. The running binary goes aside under .bad -- kept, not
	// deleted, because it is the evidence of what went wrong, and because
	// on Windows it is still mapped and cannot be removed anyway.
	bad := cfg.ExePath + badSuffix
	_ = os.Remove(bad)
	if err := os.Rename(cfg.ExePath, bad); err != nil {
		return p, fmt.Errorf("update: moving the failed binary aside: %w", err)
	}
	if err := os.Rename(prev, cfg.ExePath); err != nil {
		if rerr := os.Rename(bad, cfg.ExePath); rerr != nil {
			return p, fmt.Errorf("update: restoring %s failed (%w) and the failed binary could not be put back either (%v)", cfg.ExePath, err, rerr)
		}
		return p, fmt.Errorf("update: restoring the binary: %w", err)
	}
	_ = os.Remove(filepath.Join(cfg.StateDir, probationFile))
	_ = os.Remove(prevCfg)
	return p, nil
}

// Due reports whether a probation has run out at t.
func (p *Probation) Due(t time.Time) bool {
	return p != nil && t.After(p.Deadline)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst + ".tmp")
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst + ".tmp")
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst + ".tmp")
		return err
	}
	return os.Rename(dst+".tmp", dst)
}

// writeFileSync writes and flushes to the platter. A probation record
// that is still in the page cache when the machine reboots into the new
// binary is a probation record that never existed.
func writeFileSync(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(path + ".tmp")
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path + ".tmp")
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path + ".tmp")
		return err
	}
	return os.Rename(path+".tmp", path)
}

// swap replaces the binary at path with the one at staged, keeping what
// was there as path.prev.
//
// One implementation for every platform, because the trick is the same
// one everywhere: a running executable cannot be written over, but it can
// be renamed out of the way, on Unix and on Windows alike. Both files are
// in the same directory by construction (download stages beside the
// binary), so both renames are metadata operations that either happen or
// do not -- there is no moment where the agent is half replaced.
func swap(path, staged string) error {
	prev := path + prevSuffix
	_ = os.Remove(prev)
	if err := os.Rename(path, prev); err != nil {
		return fmt.Errorf("moving the running binary aside: %w", err)
	}
	if err := os.Rename(staged, path); err != nil {
		// Put it back. Failing here with nothing at the binary's path
		// is the one outcome worth writing recovery code for.
		if rerr := os.Rename(prev, path); rerr != nil {
			return fmt.Errorf("installing the new binary failed (%w) and the old one could not be put back (%v)", err, rerr)
		}
		return fmt.Errorf("installing the new binary: %w", err)
	}
	return nil
}
