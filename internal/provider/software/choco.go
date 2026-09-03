package software

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	p := c.path()
	if filepath.Base(p) == p {
		if _, err := exec.LookPath(p); err != nil {
			return false, "Chocolatey is not installed: " + p + " is not on PATH"
		}
		return true, ""
	}
	if _, err := os.Stat(p); err != nil {
		return false, "Chocolatey is not installed: " + p + " is missing"
	}
	return true, ""
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

// run executes choco with args under timeout seconds (0 for none). The
// Status is empty when the program ran and the exit code is its own.
func (c *Choco) run(ctx context.Context, timeout int, args ...string) Result {
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	}
	defer cancel()
	cmd := exec.CommandContext(runCtx, c.path(), args...)
	procs.Attach(cmd)
	out := procs.NewTail(MaxDetails)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	r := Result{Details: out.String()}
	switch {
	case err == nil:
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		r.Status = StatusTimeout
		r.Details = fmt.Sprintf("timed out after %ds\n%s", timeout, r.Details)
	default:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			r.ExitCode = exit.ExitCode()
		} else {
			r.Status = StatusCannotRun
			r.Details = err.Error() + "\n" + r.Details
		}
	}
	r.Details = procs.Clip(r.Details, MaxDetails)
	return r
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
