//go:build linux || darwin

package snapin

import (
	"archive/zip"
	"bytes"
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
	if !r.Fetched || r.Status != StatusRan || r.ExitCode != 3 || !strings.Contains(r.Details, "hello from snapin") {
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
	if !r.Fetched || r.Status != StatusHashMismatch {
		t.Fatalf("got %+v", r)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("payload ran despite the hash mismatch")
	}
}

// TestRunKillsOnTimeout: a snapin's timeout is enforced and reported as
// its own status, never as a program exit code.
func TestRunKillsOnTimeout(t *testing.T) {
	fetch, sum := payload("#!/bin/sh\nsleep 30\n")
	r := Run(context.Background(), Task{ID: 3, File: "x.sh", SHA512: sum, Timeout: 1}, t.TempDir(), fetch)
	if r.Status != StatusTimeout || !strings.Contains(r.Details, "timed out") {
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
	if r.Status != StatusRan || r.ExitCode != 0 || !strings.Contains(r.Details, "first:one second:two words") {
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

// packOf zips files (name -> body) into a pack payload.
func packOf(t *testing.T, files map[string]string) (Fetch, string) {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return payload(b.String())
}

// TestRunPack: a pack is unzipped, the placeholder names that folder in
// RunWith and RunWithArgs, the run starts inside it, and Args is not
// passed, as in the legacy client (forum topic 18251).
func TestRunPack(t *testing.T) {
	fetch, sum := packOf(t, map[string]string{
		"setup.sh":      ". ./lib/helper.sh; echo arg:$1 cwd:$(basename \"$PWD\") $(helper)\n",
		"lib/helper.sh": "helper() { echo helped; }\n",
	})
	r := Run(context.Background(), Task{ID: 7, File: "office.zip", SHA512: sum, Pack: true,
		RunWith: "sh", RunWithArgs: `"[FOG_SNAPIN_PATH]/setup.sh" one`, Args: "ignored"}, t.TempDir(), fetch)
	if r.Status != StatusRan || r.ExitCode != 0 || !strings.Contains(r.Details, "arg:one cwd:pack helped") {
		t.Fatalf("got %+v", r)
	}
}

// TestRunPackRefusesAnEscapingEntry: a zip entry that climbs out of the
// pack folder stops the whole pack before anything runs.
func TestRunPackRefusesAnEscapingEntry(t *testing.T) {
	dir := t.TempDir()
	fetch, sum := packOf(t, map[string]string{"../evil.sh": "exit 0\n", "setup.sh": "exit 0\n"})
	r := Run(context.Background(), Task{ID: 8, File: "p.zip", SHA512: sum, Pack: true,
		RunWith: "sh", RunWithArgs: "[FOG_SNAPIN_PATH]/setup.sh"}, filepath.Join(dir, "snapins"), fetch)
	if r.Status != StatusCannotRun || !strings.Contains(r.Details, "leaves the pack folder") {
		t.Fatalf("got %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "snapins", "evil.sh")); err == nil {
		t.Fatal("escaping entry was written")
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
	long := strings.Repeat("x", MaxDetails+100) + "\nlast line"
	got := clip(long)
	if len(got) > MaxDetails+3 || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "last line") {
		t.Fatalf("clip did not keep the tail within %d: len %d", MaxDetails, len(got))
	}
	if got := clip("  short\n"); got != "short" {
		t.Fatalf("clip trimming: %q", got)
	}
}
