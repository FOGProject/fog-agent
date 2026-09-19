//go:build windows

package snapin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
)

// TestCommandLineIsRaw: the arguments reach CreateProcess as the admin
// typed them for the legacy client. A backslash in a path survives, and
// msiexec's PROPERTY="a b" is not re-quoted as a whole.
func TestCommandLineIsRaw(t *testing.T) {
	cases := []struct {
		t    Task
		path string
		want string
	}{
		{Task{RunWith: "msiexec.exe", RunWithArgs: "/i", Args: `/qn TARGETDIR="C:\Program Files\X"`},
			`C:\ProgramData\FOG\agent\snapins\3\x.msi`,
			`msiexec.exe /i "C:\ProgramData\FOG\agent\snapins\3\x.msi" /qn TARGETDIR="C:\Program Files\X"`},
		{Task{Args: `/S /D=C:\Tools`}, `C:\snap\setup.exe`, `C:\snap\setup.exe /S /D=C:\Tools`},
	}
	for _, c := range cases {
		cmd, err := command(context.Background(), c.t, c.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := cmd.SysProcAttr.CmdLine; got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
}

// TestPackCommandLine: the placeholder becomes the pack folder with its
// backslashes intact, and %VAR% is expanded, as in the legacy client
// (forum topic 18251).
func TestPackCommandLine(t *testing.T) {
	task := Task{Pack: true, RunWith: `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`,
		RunWithArgs: `-ExecutionPolicy Bypass -File "[FOG_SNAPIN_PATH]\msoffice.ps1"`, Args: "ignored"}
	cmd, err := packCommand(context.Background(), task, `C:\ProgramData\FOG\agent\snapins\3\pack`)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("SystemRoot")
	want := root + `\System32\WindowsPowerShell\v1.0\powershell.exe -ExecutionPolicy Bypass -File "C:\ProgramData\FOG\agent\snapins\3\pack\msoffice.ps1"`
	if got := cmd.SysProcAttr.CmdLine; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestRunPackOnWindows runs a real pack through cmd.exe: the batch file
// is found through the placeholder and runs inside the pack folder.
func TestRunPackOnWindows(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("run.bat")
	io.WriteString(w, "@echo off\r\ntype lib\\data.txt\r\necho arg:%1\r\n")
	w, _ = zw.Create("lib/data.txt")
	io.WriteString(w, "sibling-read\r\n")
	zw.Close()
	body := b.String()
	sum := sha512.Sum512([]byte(body))
	fetch := func(_ context.Context, w io.Writer) error { _, err := io.WriteString(w, body); return err }
	r := Run(context.Background(), Task{ID: 9, File: "p.zip", SHA512: hex.EncodeToString(sum[:]), Pack: true,
		RunWith: "cmd.exe", RunWithArgs: `/c "[FOG_SNAPIN_PATH]\run.bat" one`}, t.TempDir(), fetch)
	if r.Status != StatusRan || r.ExitCode != 0 || !strings.Contains(r.Details, "sibling-read") || !strings.Contains(r.Details, "arg:one") {
		t.Fatalf("got %+v", r)
	}
}
