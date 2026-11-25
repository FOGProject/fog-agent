package software

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/FOGProject/fog-agent/internal/procs"
)

// Winget is the winget backend (design 0003 section 5, "winget"): machine
// scope packages from the winget source or a private REST source, run in
// the service as SYSTEM. The agent never installs winget (App Installer).
type Winget struct {
	// Path is the winget executable. Empty means the App Installer folder
	// that Available resolves by glob.
	Path string
	// resolved is the winget.exe that Available found by glob. Install,
	// Upgrade, Uninstall and Installed use it and do not glob again.
	resolved string
}

// wingetUpdateNotApplicable is APPINSTALLER_CLI_ERROR_UPDATE_NOT_APPLICABLE
// ("No applicable update found") from
// src/AppInstallerSharedLib/Public/AppInstallerErrors.h in
// microsoft/winget-cli. winget upgrade exits with it when nothing newer
// applies.
const wingetUpdateNotApplicable = 0x8A15002B

// wingetMsstoreRefusal is the detail for an entry on the msstore source,
// which does not work under SYSTEM.
const wingetMsstoreRefusal = "winget cannot use the msstore source from the service; use the winget source or a private REST source"

// Name is the backend id the server sends.
func (Winget) Name() string { return "winget" }

// wingetRoot is the Program Files folder that holds WindowsApps. Under
// WOW64 (the windows/386 build) ProgramFiles points at Program Files (x86),
// where winget is not, so ProgramW6432 wins when it is set.
func wingetRoot(programW6432, programFiles string) string {
	if programW6432 != "" {
		return programW6432
	}
	return programFiles
}

// PickWinget returns the match whose App Installer folder has the highest
// version, or "" for no match. The folder is
// Microsoft.DesktopAppInstaller_<version>_<arch>__8wekyb3d8bbwe, so the
// version is its second `_` field. It is pure so the tests need no
// filesystem.
func PickWinget(matches []string) string {
	best, bestVer := "", []int(nil)
	for _, m := range matches {
		ver := wingetFolderVersion(m)
		if best == "" || compareVersions(ver, bestVer) > 0 {
			best, bestVer = m, ver
		}
	}
	return best
}

// wingetFolderVersion reads the version of the folder that holds the
// winget.exe at path. Both separators count, so a Windows path parses on
// any OS. A part that is not a number counts as 0.
func wingetFolderVersion(path string) []int {
	dir := path[:max(strings.LastIndexAny(path, `\/`), 0)]
	folder := dir[strings.LastIndexAny(dir, `\/`)+1:]
	fields := strings.Split(folder, "_")
	if len(fields) < 2 {
		return nil
	}
	var ver []int
	for _, part := range strings.Split(fields[1], ".") {
		n, _ := strconv.Atoi(part)
		ver = append(ver, n)
	}
	return ver
}

// compareVersions compares dotted versions part by part, numerically. A
// missing part counts as 0.
func compareVersions(a, b []int) int {
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

func (w *Winget) path() string {
	if w.Path != "" {
		return w.Path
	}
	if w.resolved != "" {
		return w.resolved
	}
	return "winget"
}

// dir is the folder that holds winget.exe. winget as SYSTEM loads its
// DLLs from there, so every command runs in it. A bare command name has
// no folder.
func (w *Winget) dir() string {
	p := w.path()
	if filepath.Base(p) == p {
		return ""
	}
	return filepath.Dir(p)
}

// Available implements Backend. winget is an MSIX app and is not on the
// SYSTEM PATH, so the binary is found by glob under WindowsApps. A winget
// update moves the folder, so every call globs again.
func (w *Winget) Available() (bool, string) {
	if runtime.GOOS != "windows" {
		return false, "winget runs only on Windows"
	}
	if w.Path == "" {
		root := wingetRoot(os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles"))
		matches, _ := filepath.Glob(filepath.Join(root, "WindowsApps", "Microsoft.DesktopAppInstaller_*__8wekyb3d8bbwe", "winget.exe"))
		w.resolved = PickWinget(matches)
		if w.resolved == "" {
			return false, "winget is not installed: no Microsoft.DesktopAppInstaller folder under " + filepath.Join(root, "WindowsApps")
		}
	}
	return lookup(w.path(), "winget")
}

// WingetExportArgs is the command line Installed runs, exposed for the
// tests. It has no --source: export then covers every configured source,
// a private REST source included.
func WingetExportArgs(out string) []string {
	return []string{"export", "-o", out, "--include-versions", "--accept-source-agreements", "--disable-interactivity"}
}

// Installed implements Backend with `winget export`. `winget list` prints
// a fixed-width table that truncates long names and ids. export writes
// JSON.
func (w *Winget) Installed(ctx context.Context) (map[string]string, error) {
	f, err := os.CreateTemp("", "fog-winget-*.json")
	if err != nil {
		return nil, fmt.Errorf("winget export: %w", err)
	}
	out := f.Name()
	// winget writes the file itself. This handle must not hold it open.
	_ = f.Close()
	defer os.Remove(out)
	ctx, cancel := context.WithTimeout(ctx, ListTimeout)
	defer cancel()
	r := runCmdIn(ctx, w.dir(), w.path(), 0, WingetExportArgs(out)...)
	if r.Status == StatusCannotRun || r.Status == StatusTimeout || r.ExitCode != 0 {
		return nil, fmt.Errorf("winget export: %s", strings.TrimSpace(firstLine(r.Details)))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("winget export: %w", err)
	}
	return ParseWingetExport(data)
}

// ParseWingetExport reads a `winget export --include-versions` file and
// maps each lowercase package id, across every source, to its version.
func ParseWingetExport(data []byte) (map[string]string, error) {
	var doc struct {
		Sources []struct {
			Packages []struct {
				PackageIdentifier string
				Version           string
			}
		}
	}
	// A UTF-8 byte order mark would make the JSON invalid.
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &doc); err != nil {
		return nil, fmt.Errorf("winget export: %w", err)
	}
	m := map[string]string{}
	for _, s := range doc.Sources {
		for _, p := range s.Packages {
			if id := strings.TrimSpace(p.PackageIdentifier); id != "" {
				m[strings.ToLower(id)] = p.Version
			}
		}
	}
	return m, nil
}

// wingetSource is the entry's source, or the winget community source.
func wingetSource(e Entry) string {
	if e.Source == "" {
		return "winget"
	}
	return e.Source
}

// WingetInstallArgs is the command line Install runs, exposed for the
// tests. A pinned entry carries --version: winget holds an exact version.
func WingetInstallArgs(e Entry) []string {
	args := []string{"install", "--id", e.Package, "--exact", "--source", wingetSource(e), "--scope", "machine", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
	if IsPinned(e) {
		args = append(args, "--version", e.Version)
	}
	return append(args, procs.SplitArgs(e.Args)...)
}

// WingetUpgradeArgs is the command line Upgrade runs.
func WingetUpgradeArgs(e Entry) []string {
	args := []string{"upgrade", "--id", e.Package, "--exact", "--source", wingetSource(e), "--scope", "machine", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
	return append(args, procs.SplitArgs(e.Args)...)
}

// WingetUninstallArgs is the command line Uninstall runs.
func WingetUninstallArgs(e Entry) []string {
	return []string{"uninstall", "--id", e.Package, "--exact", "--source", wingetSource(e), "--scope", "machine", "--silent", "--accept-source-agreements", "--disable-interactivity"}
}

// wingetUpgradeExit maps "no applicable update" to 0, so Converge reports
// the entry as converged. On Windows, ExitCode() turns the uint32 status
// into an int. On amd64 and arm64 the code is 2316632107. On 386 it is
// -1978335189. The uint32 compare matches both. Every other code passes
// through unchanged for the server's return-code table.
func wingetUpgradeExit(code int) int {
	if uint32(code) == wingetUpdateNotApplicable {
		return 0
	}
	return code
}

// refuseMsstore is the answer to an entry on the msstore source. The bool
// is false for every other source.
func refuseMsstore(e Entry) (Result, bool) {
	if strings.EqualFold(e.Source, "msstore") {
		return Result{Status: StatusCannotRun, Details: wingetMsstoreRefusal}, true
	}
	return Result{}, false
}

// Install implements Backend. A pinned entry installs with --version, so
// there is no refusePin here.
func (w *Winget) Install(ctx context.Context, e Entry) Result {
	if r, refused := refuseMsstore(e); refused {
		return r
	}
	return runCmdIn(ctx, w.dir(), w.path(), e.Timeout, WingetInstallArgs(e)...)
}

// Upgrade implements Backend.
func (w *Winget) Upgrade(ctx context.Context, e Entry) Result {
	if r, refused := refuseMsstore(e); refused {
		return r
	}
	r := runCmdIn(ctx, w.dir(), w.path(), e.Timeout, WingetUpgradeArgs(e)...)
	if r.Status == "" {
		r.ExitCode = wingetUpgradeExit(r.ExitCode)
	}
	return r
}

// Uninstall implements Backend.
func (w *Winget) Uninstall(ctx context.Context, e Entry) Result {
	if r, refused := refuseMsstore(e); refused {
		return r
	}
	return runCmdIn(ctx, w.dir(), w.path(), e.Timeout, WingetUninstallArgs(e)...)
}

// errWingetNoKeep is why exclusive removal never runs for winget.
var errWingetNoKeep = errors.New("winget has no protected set: its export lists Windows components such as App Installer itself")

// Keep implements Keeper and always fails, so ConvergeExclusive removes
// nothing for winget. Design 0003 section 5, "Exclusive level", says why.
func (w *Winget) Keep(_ context.Context, _ []Entry, _ map[string]string) (map[string]bool, []string, error) {
	return nil, nil, errWingetNoKeep
}
