package software

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/FOGProject/fog-agent/internal/procs"
)

// Flatpak is the flatpak backend (design 0018): system-wide apps from a
// remote the admin has already added. The agent never installs flatpak and
// never adds a remote.
type Flatpak struct {
	// Path is the flatpak executable. Empty means `flatpak` on PATH.
	Path string
}

// Name is the backend id the server sends.
func (Flatpak) Name() string { return "flatpak" }

func (f *Flatpak) path() string {
	if f.Path != "" {
		return f.Path
	}
	return "flatpak"
}

// Available implements Backend.
func (f *Flatpak) Available() (bool, string) {
	if runtime.GOOS != "linux" {
		return false, "flatpak runs only on Linux"
	}
	return lookup(f.path(), "flatpak")
}

// Installed implements Backend with `flatpak list`, whose output is one
// `application<TAB>version` per line.
func (f *Flatpak) Installed(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, ListTimeout)
	defer cancel()
	r := runCmd(ctx, f.path(), 0, "list", "--system", "--app", "--columns=application,version")
	if r.Status == StatusCannotRun || r.Status == StatusTimeout || r.ExitCode != 0 {
		return nil, fmt.Errorf("flatpak list: %s", strings.TrimSpace(firstLine(r.Details)))
	}
	return ParseFlatpakList(r.Details), nil
}

// ParseFlatpakList reads `flatpak list --columns=application,version`
// output. The version may be empty, so a line is split on its first tab. A
// line without a tab counts as an application only when it looks like an
// id: it has a dot and no spaces. Anything else is chatter and skipped.
func ParseFlatpakList(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, ver, ok := strings.Cut(line, "\t")
		id = strings.TrimSpace(id)
		if !ok && (!strings.Contains(id, ".") || strings.ContainsAny(id, " ")) {
			continue
		}
		if id == "" {
			continue
		}
		m[strings.ToLower(id)] = strings.TrimSpace(ver)
	}
	return m
}

// FlatpakInstallArgs is the command line Install runs, exposed for the
// tests. An empty source means the flathub remote.
func FlatpakInstallArgs(e Entry) []string {
	source := e.Source
	if source == "" {
		source = "flathub"
	}
	args := []string{"install", "--system", "-y", "--noninteractive", source, e.Package}
	return append(args, procs.SplitArgs(e.Args)...)
}

// FlatpakUpgradeArgs is the command line Upgrade runs.
func FlatpakUpgradeArgs(e Entry) []string {
	args := []string{"update", "--system", "-y", "--noninteractive", e.Package}
	return append(args, procs.SplitArgs(e.Args)...)
}

// FlatpakUninstallArgs is the command line Uninstall runs.
func FlatpakUninstallArgs(e Entry) []string {
	return []string{"uninstall", "--system", "-y", "--noninteractive", e.Package}
}

// Install implements Backend. Flatpak pins by commit, not by version
// string, so an exact version is refused.
func (f *Flatpak) Install(ctx context.Context, e Entry) Result {
	if IsPinned(e) {
		return refusePin(f.Name(), e)
	}
	return runCmd(ctx, f.path(), e.Timeout, FlatpakInstallArgs(e)...)
}

// Upgrade implements Backend.
func (f *Flatpak) Upgrade(ctx context.Context, e Entry) Result {
	return runCmd(ctx, f.path(), e.Timeout, FlatpakUpgradeArgs(e)...)
}

// Uninstall implements Backend.
func (f *Flatpak) Uninstall(ctx context.Context, e Entry) Result {
	return runCmd(ctx, f.path(), e.Timeout, FlatpakUninstallArgs(e)...)
}
