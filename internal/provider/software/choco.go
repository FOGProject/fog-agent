package software

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/FOGProject/fog-agent/internal/procs"
)

// Choco is the Chocolatey backend: the choco command-line at a path. It
// is addressed as a binary rather than as "Windows" so the pipeline can
// be proven with a shim on the Linux lab before the real thing (design
// 0003 section 2).
type Choco struct {
	// Path is the choco executable. Empty means the default: the
	// ProgramData install on Windows, `choco` on PATH elsewhere, which
	// is how FOG's snapin templates already call it (the service's PATH
	// predates Chocolatey's entry in it).
	Path string
	// major is the Chocolatey major version, read once: 2.x dropped the
	// --local-only flag from list and errors on it, 1.x needs it.
	major int
	// libDir is Chocolatey's lib directory, where each installed package
	// keeps its nuspec. Empty means the default (see lib); the tests point
	// it at a temp dir.
	libDir string
}

// ListTimeout bounds the installed-list call; installs carry their own.
const ListTimeout = 2 * time.Minute

// Name is the backend id the server sends.
func (Choco) Name() string { return "choco" }

func (c *Choco) path() string {
	if c.Path != "" {
		return c.Path
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "chocolatey", "bin", "choco.exe")
	}
	return "choco"
}

// Available implements Backend.
func (c *Choco) Available() (bool, string) {
	return lookup(c.path(), "Chocolatey")
}

// Installed implements Backend with `choco list -r`, whose output is one
// `id|version` per line.
func (c *Choco) Installed(ctx context.Context) (map[string]string, error) {
	if c.major == 0 {
		c.major = c.version(ctx)
	}
	args := []string{"list", "-r"}
	if c.major < 2 {
		args = append(args, "--local-only")
	}
	ctx, cancel := context.WithTimeout(ctx, ListTimeout)
	defer cancel()
	r := c.run(ctx, 0, args...)
	if r.Status == StatusCannotRun || r.Status == StatusTimeout || r.ExitCode != 0 {
		return nil, fmt.Errorf("choco list: %s", strings.TrimSpace(firstLine(r.Details)))
	}
	return ParseList(r.Details), nil
}

// ParseList reads `choco list -r` output. Anything without a pipe is
// chatter and skipped.
func ParseList(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		id, ver, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || id == "" {
			continue
		}
		m[strings.ToLower(id)] = ver
	}
	return m
}

// version reads the major of `choco --version`; 0 when unreadable, which
// is then treated as 1.x, the form that still works on both.
func (c *Choco) version(ctx context.Context) int {
	ctx, cancel := context.WithTimeout(ctx, ListTimeout)
	defer cancel()
	r := c.run(ctx, 0, "--version")
	if r.ExitCode != 0 || r.Status != "" {
		return 1
	}
	major, _, _ := strings.Cut(strings.TrimSpace(r.Details), ".")
	n, err := strconv.Atoi(major)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// InstallArgs is the command line Install runs, exposed for the tests.
func InstallArgs(e Entry) []string {
	args := []string{"install", e.Package, "-y", "-r", "--no-progress"}
	if e.Version != "" && e.Version != VersionLatest {
		// --force makes a pinned version win over whatever is there,
		// down as well as up.
		args = append(args, "--version="+e.Version, "--force")
	}
	if e.Source != "" {
		args = append(args, "--source="+e.Source)
	}
	return append(args, procs.SplitArgs(e.Args)...)
}

// UpgradeArgs is the command line Upgrade runs.
func UpgradeArgs(e Entry) []string {
	args := []string{"upgrade", e.Package, "-y", "-r", "--no-progress"}
	if e.Source != "" {
		args = append(args, "--source="+e.Source)
	}
	return append(args, procs.SplitArgs(e.Args)...)
}

// UninstallArgs is the command line Uninstall runs.
func UninstallArgs(e Entry) []string {
	return []string{"uninstall", e.Package, "-y", "-r"}
}

// Install implements Backend.
func (c *Choco) Install(ctx context.Context, e Entry) Result {
	return c.run(ctx, e.Timeout, InstallArgs(e)...)
}

// Upgrade implements Backend.
func (c *Choco) Upgrade(ctx context.Context, e Entry) Result {
	return c.run(ctx, e.Timeout, UpgradeArgs(e)...)
}

// Uninstall implements Backend.
func (c *Choco) Uninstall(ctx context.Context, e Entry) Result {
	return c.run(ctx, e.Timeout, UninstallArgs(e)...)
}

// lib is the directory holding one subdirectory per installed package:
// ChocolateyInstall\lib, else the ProgramData install.
func (c *Choco) lib() string {
	if c.libDir != "" {
		return c.libDir
	}
	if base := os.Getenv("ChocolateyInstall"); base != "" {
		return filepath.Join(base, "lib")
	}
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "chocolatey", "lib")
}

// nuspec is the part of a package's .nuspec that names its dependencies.
// Without a namespace on the tags, encoding/xml matches any namespace, which
// is what is wanted: the nuspec schema URI differs between packages.
type nuspec struct {
	Deps      []nuspecDep `xml:"metadata>dependencies>dependency"`
	GroupDeps []nuspecDep `xml:"metadata>dependencies>group>dependency"`
}

type nuspecDep struct {
	ID string `xml:"id,attr"`
}

// ParseNuspecDeps returns the lowercase ids a nuspec depends on, plain and
// grouped by target framework alike.
func ParseNuspecDeps(data []byte) ([]string, error) {
	var n nuspec
	if err := xml.Unmarshal(data, &n); err != nil {
		return nil, err
	}
	var out []string
	for _, d := range append(n.Deps, n.GroupDeps...) {
		if id := strings.ToLower(strings.TrimSpace(d.ID)); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// deps reads the dependencies of an installed package from its nuspec.
func (c *Choco) deps(id string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(c.lib(), id, id+".nuspec"))
	if err != nil {
		return nil, err
	}
	out, err := ParseNuspecDeps(data)
	if err != nil {
		return nil, fmt.Errorf("%s.nuspec: %w", id, err)
	}
	return out, nil
}

// isChocoPlumbing says whether id is Chocolatey itself or one of its
// extensions, which a package's own install scripts rely on.
func isChocoPlumbing(id string) bool {
	return id == "chocolatey" || (strings.HasPrefix(id, "chocolatey-") && strings.HasSuffix(id, ".extension"))
}

// Keep implements Keeper: Chocolatey and its extensions, plus the
// dependency closure of every assigned present package that is installed
// (and of the kept plumbing). Dependencies are read from the nuspecs. A
// nuspec that cannot be read for a package in the closure is an error, so
// nothing is removed on a guess.
func (c *Choco) Keep(_ context.Context, assigned []Entry, installed map[string]string) (map[string]bool, []string, error) {
	keep := map[string]bool{}
	var queue []string
	add := func(id string) {
		if id != "" && !keep[id] {
			keep[id] = true
			queue = append(queue, id)
		}
	}
	for id := range installed {
		if isChocoPlumbing(id) {
			add(id)
		}
	}
	add("chocolatey")
	for _, e := range assigned {
		id := strings.ToLower(e.Package)
		if _, there := installed[id]; there && e.State != StateAbsent {
			add(id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, there := installed[id]; !there {
			// A dependency that is not installed (Chocolatey itself on a
			// shimmed host, a virtual package) has no nuspec to follow.
			continue
		}
		deps, err := c.deps(id)
		if err != nil {
			// Chocolatey and its extensions are kept by name, so an
			// unreadable nuspec only means their dependencies are not
			// followed. Anywhere else the closure is unknown: fail closed.
			if isChocoPlumbing(id) {
				continue
			}
			return nil, nil, fmt.Errorf("dependencies of %s: %w", id, err)
		}
		for _, d := range deps {
			add(d)
		}
	}
	return keep, c.removalOrder(Unwanted(ManageExclusive, assigned, installed, keep)), nil
}

// removalOrder puts a package before the packages it depends on, so
// Chocolatey does not refuse to remove a dependency that is still needed.
// Any unreadable nuspec gives nil, the plain sorted order: that is not a
// reason to stop removing.
func (c *Choco) removalOrder(unwanted []string) []string {
	in := make(map[string]bool, len(unwanted))
	for _, id := range unwanted {
		in[id] = true
	}
	// needs[x] counts the not yet removed packages that depend on x.
	needs := make(map[string]int, len(unwanted))
	deps := make(map[string][]string, len(unwanted))
	for _, id := range unwanted {
		d, err := c.deps(id)
		if err != nil {
			return nil
		}
		for _, x := range d {
			if in[x] && x != id {
				deps[id] = append(deps[id], x)
				needs[x]++
			}
		}
	}
	var out []string
	done := map[string]bool{}
	for len(out) < len(unwanted) {
		progressed := false
		for _, id := range unwanted {
			if done[id] || needs[id] > 0 {
				continue
			}
			done[id] = true
			out = append(out, id)
			for _, x := range deps[id] {
				needs[x]--
			}
			progressed = true
		}
		if !progressed {
			// A dependency cycle: the rest in sorted order.
			for _, id := range unwanted {
				if !done[id] {
					out = append(out, id)
				}
			}
		}
	}
	return out
}

// run executes choco with args under timeout seconds (0 for none).
func (c *Choco) run(ctx context.Context, timeout int, args ...string) Result {
	return runCmd(ctx, c.path(), timeout, args...)
}
