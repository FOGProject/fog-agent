package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

func TestDirArg(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"run"}, enroll.DefaultDir},
		{[]string{"run", "--dir", `D:\fog`}, `D:\fog`},
		{[]string{"run", "-dir", `D:\fog`}, `D:\fog`},
		{[]string{"run", `--dir=D:\fog`}, `D:\fog`},
		{[]string{"run", "--dir"}, enroll.DefaultDir},
	}
	for _, c := range cases {
		if got := dirArg(c.args); got != c.want {
			t.Errorf("%q: got %q, want %q", c.args, got, c.want)
		}
	}
}

func TestOpenLogRollsPastKeep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	path := logPath(dir)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", logKeep+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("new\n")
	f.Close()
	if b, _ := os.ReadFile(path); string(b) != "new\n" {
		t.Errorf("log was not rolled: %d bytes", len(b))
	}
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != logKeep+1 {
		t.Errorf("old log not kept as .1: %v", err)
	}
	// Under the limit it appends and rolls nothing.
	f, _ = openLog(dir)
	f.WriteString("more\n")
	f.Close()
	if b, _ := os.ReadFile(path); string(b) != "new\nmore\n" {
		t.Errorf("append lost lines: %q", b)
	}
}

// The log sits beside the state directory, never inside it: the state
// directory is locked down, the log must stay readable.
func TestLogPathBesideStateDir(t *testing.T) {
	got := logPath(filepath.Join("C:", "ProgramData", "FOG", "agent"))
	want := filepath.Join("C:", "ProgramData", "FOG", logName)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
