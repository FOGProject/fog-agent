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

// snapPlumbingNotes are the `snap list` Notes values that mark a snap other
// snaps or the system run on.
var snapPlumbingNotes = map[string]bool{"base": true, "core": true, "snapd": true, "kernel": true, "gadget": true}

// ParseSnapPlumbing reads `snap list` output and returns the lowercase names
// whose Notes column (the sixth, comma-separated) holds a plumbing note:
// base, core, snapd, kernel or gadget.
func ParseSnapPlumbing(out string) map[string]bool {
	m := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "Name" {
			continue
		}
		for _, note := range strings.Split(fields[5], ",") {
			if snapPlumbingNotes[note] {
				m[strings.ToLower(fields[0])] = true
				break
			}
		}
	}
	return m
}

// ParseSnapSlotProviders reads `snap connections <name>` output and returns
// the lowercase names of the snaps that provide a slot the snap is
// connected to: the Slot column is `provider:slot`, `:slot` for the system
// itself, or `-` when unconnected.
func ParseSnapSlotProviders(out string) map[string]bool {
	m := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "Interface" {
			continue
		}
		slot := fields[2]
		if slot == "-" || strings.HasPrefix(slot, ":") {
			continue
		}
		if name, _, ok := strings.Cut(slot, ":"); ok && name != "" {
			m[strings.ToLower(name)] = true
		}
	}
	return m
}

// Keep implements Keeper: every base, core, snapd, kernel and gadget snap,
// plus the providers of the slots an assigned present snap is connected to
// (content snaps). A command that fails is an error, so nothing is removed
// on a guess.
func (s *Snap) Keep(ctx context.Context, assigned []Entry, installed map[string]string) (map[string]bool, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, ListTimeout)
	defer cancel()
	r := runCmd(ctx, s.path(), 0, "list")
	if r.Status == StatusCannotRun || r.Status == StatusTimeout || r.ExitCode != 0 {
		return nil, nil, fmt.Errorf("snap list: %s", strings.TrimSpace(firstLine(r.Details)))
	}
	keep := ParseSnapPlumbing(r.Details)
	for _, e := range assigned {
		name := strings.ToLower(e.Package)
		if _, there := installed[name]; !there || e.State == StateAbsent {
			continue
		}
		r := runCmd(ctx, s.path(), 0, "connections", name)
		if r.Status == StatusCannotRun || r.Status == StatusTimeout || r.ExitCode != 0 {
			return nil, nil, fmt.Errorf("snap connections %s: %s", name, strings.TrimSpace(firstLine(r.Details)))
		}
		for p := range ParseSnapSlotProviders(r.Details) {
			keep[p] = true
		}
	}
	return keep, nil, nil
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
