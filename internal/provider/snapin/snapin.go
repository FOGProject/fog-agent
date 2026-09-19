// Package snapin runs FOG snapins: payload-only software (design 0001
// section 7). A task's file is fetched over the agent's own session,
// refused unless its sha512 matches what the server declared, run with
// the interpreter and arguments the snapin carries, and its exit code
// and output tail reported. Nothing here reboots; a snapin's reboot or
// shutdown flag is the reboot coordinator's business.
package snapin

import (
	"archive/zip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/FOGProject/fog-agent/internal/procs"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Task is one entry of the desired state's snapins block, in run order.
type Task struct {
	ID          int    `json:"task"`
	SnapinID    int    `json:"snapin"`
	Name        string `json:"name"`
	File        string `json:"file"`
	Size        int64  `json:"size"`
	SHA512      string `json:"sha512"`
	Args        string `json:"args"`
	RunWith     string `json:"run_with"`
	RunWithArgs string `json:"run_with_args"`
	Timeout     int    `json:"timeout"` // seconds, 0 for none
	Action      string `json:"action"`  // "", "reboot", "shutdown"
	AbortOnFail bool   `json:"abort_on_fail"`
	// Pack marks a snapin pack: File is a zip, and the run is RunWith
	// with RunWithArgs inside the unzipped folder, which Placeholder
	// names. Args is not used, as in the legacy client.
	Pack bool `json:"pack"`
}

// Placeholder is the token a pack's RunWith and RunWithArgs use for the
// folder the pack is unzipped to. The server's pack templates put the
// separator after it ("[FOG_SNAPIN_PATH]\setup.exe").
const Placeholder = "[FOG_SNAPIN_PATH]"

// Statuses: whether the payload ran at all. The exit code is the program's
// own and is only meaningful for StatusRan; the server maps it to an
// outcome (success, reboot, retry, failed) with the snapin's return-code
// table, the way Intune and SCCM read installer codes. Everything else
// here is the agent's failure to run it, named rather than encoded as a
// number that could collide with a program's.
const (
	StatusRan          = "ran"
	StatusHashMismatch = "hash_mismatch"
	StatusTimeout      = "timeout"
	StatusCannotRun    = "cannot_run"
)

// MaxDetails is how much of the output tail is reported. The server's
// column is TEXT; this is the agent's own bound on what it sends.
const MaxDetails = 4096

// Result is what one task came to.
type Result struct {
	// Fetched is false when the payload never arrived: nothing ran, the
	// task stays open, and the next poll tries again.
	Fetched  bool
	Status   string
	ExitCode int
	Details  string
}

// Fetch writes the payload bytes to w.
type Fetch func(ctx context.Context, w io.Writer) error

// Run fetches, verifies and runs one task under dir, which it creates and
// leaves clean. It reports; it never decides what happens next.
func Run(ctx context.Context, t Task, dir string, fetch Fetch) Result {
	work := filepath.Join(dir, strconv.Itoa(t.ID))
	if err := os.MkdirAll(work, 0o700); err != nil {
		return Result{Fetched: true, Status: StatusCannotRun, Details: "workdir: " + err.Error()}
	}
	defer os.RemoveAll(work)
	path := filepath.Join(work, filepath.Base(t.File))
	sum, err := download(ctx, path, fetch)
	if err != nil {
		return Result{Details: "fetch: " + err.Error()}
	}
	if !strings.EqualFold(sum, t.SHA512) {
		// The one refusal that is a result rather than a retry: the file
		// the server has is not the file it described, and fetching it
		// again will not change that. Closing the task with this code is
		// what lets the admin see it.
		return Result{Fetched: true, Status: StatusHashMismatch, Details: fmt.Sprintf(
			"sha512 mismatch: server says %.16s…, payload is %.16s…", t.SHA512, sum)}
	}
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if t.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(t.Timeout)*time.Second)
	}
	defer cancel()
	var cmd *exec.Cmd
	if t.Pack {
		// A pack runs in its own unzipped folder, as the legacy client
		// ran it, so a script can reach its siblings by relative path.
		pack := filepath.Join(work, "pack")
		if err = unzip(path, pack); err != nil {
			return Result{Fetched: true, Status: StatusCannotRun, Details: "unzip: " + err.Error()}
		}
		if cmd, err = packCommand(runCtx, t, pack); err == nil {
			cmd.Dir = pack
		}
	} else if cmd, err = command(runCtx, t, path); err == nil {
		cmd.Dir = work
	}
	if err != nil {
		return Result{Fetched: true, Status: StatusCannotRun, Details: err.Error()}
	}
	out := procs.NewTail(MaxDetails)
	cmd.Stdout, cmd.Stderr = out, out
	// A payload that spawned children and was killed leaves them holding
	// the output pipe; do not wait on them past the kill.
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	r := Result{Fetched: true, Status: StatusRan, Details: out.String()}
	switch {
	case err == nil:
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		r.Status = StatusTimeout
		r.Details = fmt.Sprintf("timed out after %ds\n%s", t.Timeout, r.Details)
	default:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			r.ExitCode = exit.ExitCode()
		} else {
			r.Status = StatusCannotRun
			r.Details = err.Error() + "\n" + r.Details
		}
	}
	r.Details = clip(r.Details)
	return r
}

// download streams the payload to path, hashing as it goes, and returns
// the sha512 as lowercase hex.
func download(ctx context.Context, path string, fetch Fetch) (string, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	h := sha512.New()
	err = fetch(ctx, io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unzip extracts the pack at path into dir. An entry whose name would
// land outside dir is refused, not skipped: the pack is not what it
// claims to be, and running part of it is worse than running none.
func unzip(path, dir string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("entry %q leaves the pack folder", f.Name)
		}
		target := filepath.Join(dir, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := extract(f, target); err != nil {
			return err
		}
	}
	return nil
}

// extract writes one entry, keeping its permission bits so a packed
// script stays executable on Unix.
func extract(f *zip.File, target string) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

// tail keeps the last max bytes written: the end of the output is where
// the error is, and the server keeps 250 characters of it anyway.
// tail, clip and splitArgs are procs' helpers under the names this
// package's tests know them by.
type tail = procs.Tail

func clip(s string) string { return procs.Clip(s, MaxDetails) }

func splitArgs(s string) []string { return procs.SplitArgs(s) }
