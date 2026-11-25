package software

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestBackendForWinget(t *testing.T) {
	if _, ok := BackendFor("winget").(*Winget); !ok {
		t.Errorf("BackendFor(winget) = %T, want *Winget", BackendFor("winget"))
	}
}

// The highest version wins numerically, so 1.10 beats 1.9 although it
// sorts lower as a string, and the order of the matches does not matter.
func TestPickWinget(t *testing.T) {
	const (
		v19  = `C:\Program Files\WindowsApps\Microsoft.DesktopAppInstaller_1.9.25200.0_x64__8wekyb3d8bbwe\winget.exe`
		v110 = `C:\Program Files\WindowsApps\Microsoft.DesktopAppInstaller_1.10.1.0_x64__8wekyb3d8bbwe\winget.exe`
		v12  = `C:\Program Files\WindowsApps\Microsoft.DesktopAppInstaller_1.2.10691.0_arm64__8wekyb3d8bbwe\winget.exe`
	)
	for _, matches := range [][]string{{v19, v110, v12}, {v110, v12, v19}, {v12, v19, v110}} {
		if got := PickWinget(matches); got != v110 {
			t.Errorf("PickWinget(%q) = %q, want %q", matches, got, v110)
		}
	}
}

func TestPickWingetNone(t *testing.T) {
	if got := PickWinget(nil); got != "" {
		t.Errorf("PickWinget(nil) = %q, want empty", got)
	}
}

// Two sources and mixed-case ids, in the shape winget export writes.
const wingetExportJSON = `{
  "$schema": "https://aka.ms/winget-packages.schema.2.0.json",
  "CreationDate": "2026-11-25T10:00:00.000-00:00",
  "Sources": [
    {
      "Packages": [
        {"PackageIdentifier": "Mozilla.Firefox", "Version": "133.0"},
        {"PackageIdentifier": "7zip.7zip", "Version": "24.08"}
      ],
      "SourceDetails": {"Argument": "https://cdn.winget.microsoft.com/cache", "Identifier": "Microsoft.Winget.Source_8wekyb3d8bbwe", "Name": "winget", "Type": "Microsoft.PreIndexed.Package"}
    },
    {
      "Packages": [
        {"PackageIdentifier": "Contoso.InternalTool", "Version": "2.1.0"}
      ],
      "SourceDetails": {"Argument": "https://packages.contoso.example/api", "Identifier": "contoso", "Name": "contoso", "Type": "Microsoft.Rest"}
    }
  ],
  "WinGetVersion": "1.10.1"
}`

func TestParseWingetExport(t *testing.T) {
	got, err := ParseWingetExport([]byte(wingetExportJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"mozilla.firefox": "133.0", "7zip.7zip": "24.08", "contoso.internaltool": "2.1.0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseWingetExportInvalid(t *testing.T) {
	if _, err := ParseWingetExport([]byte(`{"Sources": [`)); err == nil {
		t.Error("invalid JSON: got no error")
	}
}

func TestWingetExportArgs(t *testing.T) {
	want := []string{"export", "-o", `C:\Temp\x.json`, "--include-versions", "--accept-source-agreements", "--disable-interactivity"}
	got := WingetExportArgs(`C:\Temp\x.json`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, a := range got {
		if a == "--source" {
			t.Error("export has --source; it must cover every source")
		}
	}
}

func TestWingetArgs(t *testing.T) {
	common := []string{"--scope", "machine", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
	with := func(head []string, tail ...string) []string {
		return append(append(append([]string{}, head...), common...), tail...)
	}
	cases := []struct {
		name string
		e    Entry
		inst []string
		upg  []string
		rm   []string
	}{
		{
			name: "default source",
			e:    Entry{Package: "Mozilla.Firefox", Version: VersionLatest},
			inst: with([]string{"install", "--id", "Mozilla.Firefox", "--exact", "--source", "winget"}),
			upg:  with([]string{"upgrade", "--id", "Mozilla.Firefox", "--exact", "--source", "winget"}),
			rm:   []string{"uninstall", "--id", "Mozilla.Firefox", "--exact", "--source", "winget", "--scope", "machine", "--silent", "--accept-source-agreements", "--disable-interactivity"},
		},
		{
			name: "custom source",
			e:    Entry{Package: "Contoso.Tool", Source: "contoso"},
			inst: with([]string{"install", "--id", "Contoso.Tool", "--exact", "--source", "contoso"}),
			upg:  with([]string{"upgrade", "--id", "Contoso.Tool", "--exact", "--source", "contoso"}),
			rm:   []string{"uninstall", "--id", "Contoso.Tool", "--exact", "--source", "contoso", "--scope", "machine", "--silent", "--accept-source-agreements", "--disable-interactivity"},
		},
		{
			name: "pinned with extra args",
			e:    Entry{Package: "Mozilla.Firefox", Version: "132.0", Args: `--override "/S /x"`},
			inst: with([]string{"install", "--id", "Mozilla.Firefox", "--exact", "--source", "winget"}, "--version", "132.0", "--override", "/S /x"),
			upg:  with([]string{"upgrade", "--id", "Mozilla.Firefox", "--exact", "--source", "winget"}, "--override", "/S /x"),
			rm:   []string{"uninstall", "--id", "Mozilla.Firefox", "--exact", "--source", "winget", "--scope", "machine", "--silent", "--accept-source-agreements", "--disable-interactivity"},
		},
	}
	for _, c := range cases {
		if got := WingetInstallArgs(c.e); !reflect.DeepEqual(got, c.inst) {
			t.Errorf("%s install: got %q, want %q", c.name, got, c.inst)
		}
		if got := WingetUpgradeArgs(c.e); !reflect.DeepEqual(got, c.upg) {
			t.Errorf("%s upgrade: got %q, want %q", c.name, got, c.upg)
		}
		if got := WingetUninstallArgs(c.e); !reflect.DeepEqual(got, c.rm) {
			t.Errorf("%s uninstall: got %q, want %q", c.name, got, c.rm)
		}
	}
}

// msstore is refused before anything runs: the path does not exist, so a
// run would say something else.
func TestWingetRefusesMsstore(t *testing.T) {
	w := &Winget{Path: "/nonexistent/winget.exe"}
	ctx := context.Background()
	want := "winget cannot use the msstore source from the service; use the winget source or a private REST source"
	for _, src := range []string{"msstore", "MSStore"} {
		e := Entry{Package: "9NBLGGH4NNS1", Source: src}
		for name, r := range map[string]Result{"install": w.Install(ctx, e), "upgrade": w.Upgrade(ctx, e), "uninstall": w.Uninstall(ctx, e)} {
			if r.Status != StatusCannotRun || r.Details != want {
				t.Errorf("%s %s: got %s %q, want cannot_run %q", name, src, r.Status, r.Details, want)
			}
		}
	}
}

func TestWingetUpgradeExit(t *testing.T) {
	// The uint32 forms overflow a 32-bit int as constants (windows/386), so
	// they convert at run time, where 386 wraps them as ExitCode() would.
	notApplicable, next := int64(2316632107), int64(2316632108)
	for _, c := range []struct{ in, want int }{
		{int(notApplicable), 0},
		{-1978335189, 0},
		{0, 0},
		{1, 1},
		{int(next), int(next)},
	} {
		if got := wingetUpgradeExit(c.in); got != c.want {
			t.Errorf("wingetUpgradeExit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestWingetRoot(t *testing.T) {
	if got := wingetRoot(`C:\Program Files`, `C:\Program Files (x86)`); got != `C:\Program Files` {
		t.Errorf("both set: got %q", got)
	}
	if got := wingetRoot("", `C:\Program Files`); got != `C:\Program Files` {
		t.Errorf("no ProgramW6432: got %q", got)
	}
}

func TestWingetAvailableOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has winget")
	}
	ok, why := (&Winget{}).Available()
	if ok || why != "winget runs only on Windows" {
		t.Errorf("got %v %q", ok, why)
	}
}

// Exclusive removal never runs for winget: Keep fails, so nothing goes.
func TestWingetExclusiveRemovesNothing(t *testing.T) {
	if _, _, err := (&Winget{}).Keep(context.Background(), nil, nil); err == nil {
		t.Error("Keep: got no error; exclusive removal would run for winget")
	}
}

// Installed runs the export in winget's own folder, reads the file it
// wrote, and removes it. A shell shim stands in for winget.exe.
func TestWingetInstalledShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shim is a shell script")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "winget")
	log := filepath.Join(t.TempDir(), "log")
	// $3 is the -o path. pwd proves the run directory.
	script := "#!/bin/sh\npwd > " + log + "\necho \"$3\" >> " + log + "\ncat > \"$3\" <<'EOF'\n" + wingetExportJSON + "\nEOF\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := (&Winget{Path: shim}).Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["contoso.internaltool"] != "2.1.0" || len(got) != 3 {
		t.Errorf("got %v", got)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("shim log %q", data)
	}
	if want, _ := filepath.EvalSymlinks(dir); lines[0] != dir && lines[0] != want {
		t.Errorf("ran in %q, want %q", lines[0], dir)
	}
	if _, err := os.Stat(lines[1]); !os.IsNotExist(err) {
		t.Errorf("export file %q left behind: %v", lines[1], err)
	}
}
