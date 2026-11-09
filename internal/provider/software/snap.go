package software

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/FOGProject/fog-agent/internal/procs"
)

// Snap is the snap backend (design 0018). The agent never installs snapd.
type Snap struct {
	// Path is the snap executable. Empty means `snap` on PATH.
	Path string
}

// Name is the backend id the server sends.
func (Snap) Name() string { return "snap" }

func (s *Snap) path() string {
	if s.Path != "" {
		return s.Path
	}
	return "snap"
}

// Available implements Backend.
func (s *Snap) Available() (bool, string) {
	if runtime.GOOS != "linux" {
		return false, "snap runs only on Linux"
	}
	return lookup(s.path(), "snap")
}

// Installed implements Backend with `snap list`.
func (s *Snap) Installed(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, ListTimeout)
	defer cancel()
	r := runCmd(ctx, s.path(), 0, "list")
	if r.Status == StatusCannotRun || r.Status == StatusTimeout || r.ExitCode != 0 {
		return nil, fmt.Errorf("snap list: %s", strings.TrimSpace(firstLine(r.Details)))
	}
	return ParseSnapList(r.Details), nil
}

// ParseSnapList reads `snap list` output: a header line, then one snap per
// line in whitespace columns. The name is the first column and the version
// the second.
func ParseSnapList(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "Name" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "No snaps are installed yet.") {
			continue
		}
		m[strings.ToLower(fields[0])] = fields[1]
	}
	return m
}

// channelArg is the --channel flag for a source, or nothing when the
// source is empty and the snap's default channel applies.
func channelArg(e Entry) []string {
	if e.Source == "" {
		return nil
	}
	return []string{"--channel=" + e.Source}
}

// SnapInstallArgs is the command line Install runs, exposed for the tests.
func SnapInstallArgs(e Entry) []string {
	args := append([]string{"install", e.Package}, channelArg(e)...)
	return append(args, procs.SplitArgs(e.Args)...)
}

// SnapUpgradeArgs is the command line Upgrade runs.
func SnapUpgradeArgs(e Entry) []string {
	args := append([]string{"refresh", e.Package}, channelArg(e)...)
	return append(args, procs.SplitArgs(e.Args)...)
}

// SnapUninstallArgs is the command line Uninstall runs.
func SnapUninstallArgs(e Entry) []string {
	return []string{"remove", e.Package}
}

// Install implements Backend. Snap pins by revision and snaps refresh on
// their own, so an exact version is refused.
func (s *Snap) Install(ctx context.Context, e Entry) Result {
	if IsPinned(e) {
		return refusePin(s.Name(), e)
	}
	return runCmd(ctx, s.path(), e.Timeout, SnapInstallArgs(e)...)
}

// Upgrade implements Backend.
func (s *Snap) Upgrade(ctx context.Context, e Entry) Result {
	return runCmd(ctx, s.path(), e.Timeout, SnapUpgradeArgs(e)...)
}

// Uninstall implements Backend.
func (s *Snap) Uninstall(ctx context.Context, e Entry) Result {
	return runCmd(ctx, s.path(), e.Timeout, SnapUninstallArgs(e)...)
}
