//go:build linux || darwin

package snapin

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func payload(body string) (Fetch, string) {
	sum := sha512.Sum512([]byte(body))
	return func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, body)
		return err
	}, hex.EncodeToString(sum[:])
}

// TestRunReportsTheExitCodeAndOutput: the payload runs as itself when no
// interpreter is named, and what comes back is its exit code and the end
// of what it printed.
func TestRunReportsTheExitCodeAndOutput(t *testing.T) {
	fetch, sum := payload("#!/bin/sh\necho hello from snapin; exit 3\n")
	r := Run(context.Background(), Task{ID: 1, File: "x.sh", SHA512: sum}, t.TempDir(), fetch)
	if !r.Fetched || r.ExitCode != 3 || !strings.Contains(r.Details, "hello from snapin") {
		t.Fatalf("got %+v", r)
	}
}

// TestRunRefusesAHashMismatch pins the design's payload rule: a file that
// does not match the declared sha512 is never executed, and the refusal
// is a closed result rather than a retry.
func TestRunRefusesAHashMismatch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	fetch, _ := payload("#!/bin/sh\ntouch " + marker + "\n")
	r := Run(context.Background(), Task{ID: 2, File: "x.sh", SHA512: strings.Repeat("0", 128)}, t.TempDir(), fetch)
	if !r.Fetched || r.ExitCode != ExitHashMismatch {
		t.Fatalf("got %+v", r)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("payload ran despite the hash mismatch")
	}
}

// TestRunKillsOnTimeout: a snapin's timeout is enforced and reported as
// its own exit code, not as the program's.
func TestRunKillsOnTimeout(t *testing.T) {
	fetch, sum := payload("#!/bin/sh\nsleep 30\n")
	r := Run(context.Background(), Task{ID: 3, File: "x.sh", SHA512: sum, Timeout: 1}, t.TempDir(), fetch)
	if r.ExitCode != ExitTimeout || !strings.Contains(r.Details, "timed out") {
		t.Fatalf("got %+v", r)
	}
}

// TestRunLeavesAnUnfetchedTaskOpen: a failed download is not a result.
func TestRunLeavesAnUnfetchedTaskOpen(t *testing.T) {
	fetch := func(context.Context, io.Writer) error { return errors.New("connection reset") }
	r := Run(context.Background(), Task{ID: 4, File: "x.sh"}, t.TempDir(), fetch)
	if r.Fetched {
		t.Fatalf("got %+v", r)
	}
}

// TestRunWithInterpreter: the interpreter gets its own arguments, then
// the payload, then the snapin's arguments, in that order.
func TestRunWithInterpreter(t *testing.T) {
	fetch, sum := payload("echo first:$1 second:$2\n")
	r := Run(context.Background(), Task{ID: 5, File: "x.sh", SHA512: sum, RunWith: "sh", RunWithArgs: "-e", Args: `one "two words"`}, t.TempDir(), fetch)
	if r.ExitCode != 0 || !strings.Contains(r.Details, "first:one second:two words") {
		t.Fatalf("got %+v", r)
	}
}

// TestRunCleansUp: the payload directory does not outlive the run.
func TestRunCleansUp(t *testing.T) {
	dir := t.TempDir()
	fetch, sum := payload("#!/bin/sh\nexit 0\n")
	Run(context.Background(), Task{ID: 6, File: "x.sh", SHA512: sum}, dir, fetch)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("payload left behind: %v", entries)
	}
}

func TestSplitArgs(t *testing.T) {
	cases := map[string][]string{
		``:                      nil,
		`/quiet /norestart`:     {"/quiet", "/norestart"},
		`a "b c" 'd e' f\ g`:    {"a", "b c", "d e", "f g"},
		`-Command "Write 'hi'"`: {"-Command", "Write 'hi'"},
		`  spaced   out  `:      {"spaced", "out"},
		`""`:                    {""},
	}
	for in, want := range cases {
		if got := splitArgs(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitArgs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	long := strings.Repeat("x", 300)
	if got := clip(long); len(got) > MaxDetails+2 || !strings.HasPrefix(got, "…") {
		t.Fatalf("clip did not keep the tail within %d: %d", MaxDetails, len(got))
	}
	if got := clip("a\n  b\tc"); got != "a b c" {
		t.Fatalf("clip folding: %q", got)
	}
}
