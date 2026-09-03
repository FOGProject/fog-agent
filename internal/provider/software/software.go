// Package software is the software capability (design 0003): a desired set
// of packages the host is converged toward through a package manager,
// with detection done by the manager's own installed list. Chocolatey is
// the first Backend; the convergence logic knows nothing about it.
package software

import (
	"context"
	"fmt"
	"strings"
)

// Entry is one package the server wants held in a state on this host.
type Entry struct {
	ID      int    `json:"id"`
	Backend string `json:"backend"`
	Package string `json:"package"`
	// Version is "" for any version, "latest" to track the source, or an
	// exact version to pin.
	Version string `json:"version"`
	// State is present or absent.
	State   string `json:"state"`
	Source  string `json:"source"`
	Args    string `json:"args"`
	Timeout int    `json:"timeout"`
}

// Policy is the software block of the desired state.
type Policy struct {
	// DriftInterval is how often, in seconds, the host re-checks the set
	// without the revision having moved.
	DriftInterval int     `json:"drift_interval"`
	Entries       []Entry `json:"entries"`
}

// The version policies and states the server sends.
const (
	VersionLatest = "latest"
	StatePresent  = "present"
	StateAbsent   = "absent"
)

// What the agent reports for one entry. The four action statuses carry
// the backend's exit code and the server reads it against the entry's
// return-code table; converged means nothing needed doing.
const (
	StatusConverged = "converged"
	StatusInstalled = "installed"
	StatusUpgraded  = "upgraded"
	StatusRemoved   = "removed"
	StatusTimeout   = "timeout"
	StatusCannotRun = "cannot_run"
)

// MaxDetails is how much of the backend's output tail is reported.
const MaxDetails = 4096

// Action is what convergence decided for one entry.
type Action int

// The actions.
const (
	None Action = iota
	Install
	Upgrade
	Uninstall
)

func (a Action) String() string {
	switch a {
	case Install:
		return "install"
	case Upgrade:
		return "upgrade"
	case Uninstall:
		return "uninstall"
	}
	return "none"
}

// Result is what a backend action came back with.
type Result struct {
	Status   string
	ExitCode int
	Details  string
}

// Backend is a package manager.
type Backend interface {
	// Available says whether the manager can be run here at all; the
	// detail names what is missing when it cannot.
	Available() (bool, string)
	// Installed maps package id to installed version.
	Installed(ctx context.Context) (map[string]string, error)
	Install(ctx context.Context, e Entry) Result
	Upgrade(ctx context.Context, e Entry) Result
	Uninstall(ctx context.Context, e Entry) Result
}

// Decide is the convergence rule for one entry against what is installed.
// It is pure so every row of the rule is a test case.
func Decide(e Entry, installed map[string]string) (Action, string) {
	have, present := installed[strings.ToLower(e.Package)]
	if e.State == StateAbsent {
		if present {
			return Uninstall, fmt.Sprintf("%s %s is installed and should not be", e.Package, have)
		}
		return None, e.Package + " is absent"
	}
	if !present {
		if e.Version != "" && e.Version != VersionLatest {
			return Install, fmt.Sprintf("%s is not installed; want %s", e.Package, e.Version)
		}
		return Install, e.Package + " is not installed"
	}
	switch e.Version {
	case "":
		return None, fmt.Sprintf("%s %s is installed", e.Package, have)
	case VersionLatest:
		return Upgrade, fmt.Sprintf("%s %s is installed; tracking latest", e.Package, have)
	}
	if strings.EqualFold(have, e.Version) {
		return None, fmt.Sprintf("%s %s is installed as pinned", e.Package, have)
	}
	return Install, fmt.Sprintf("%s %s is installed; pinned to %s", e.Package, have, e.Version)
}

// Report is one entry's result, ready to post.
type Report struct {
	Entry            Entry
	Action           Action
	Status           string
	InstalledVersion string
	ExitCode         int
	Details          string
}

// Converge runs the set in order against one backend and returns a report
// per entry. One installed-list call before, one after when anything
// ran, so every report carries the version the host actually ended with.
func Converge(ctx context.Context, b Backend, entries []Entry) []Report {
	reports := make([]Report, 0, len(entries))
	if ok, why := b.Available(); !ok {
		for _, e := range entries {
			reports = append(reports, Report{Entry: e, Status: StatusCannotRun, Details: why})
		}
		return reports
	}
	installed, err := b.Installed(ctx)
	if err != nil {
		for _, e := range entries {
			reports = append(reports, Report{Entry: e, Status: StatusCannotRun, Details: "installed list: " + err.Error()})
		}
		return reports
	}
	acted := false
	for _, e := range entries {
		action, why := Decide(e, installed)
		r := Report{Entry: e, Action: action, Status: StatusConverged, Details: why}
		switch action {
		case Install:
			r.Status, r.ExitCode, r.Details = fill(StatusInstalled, b.Install(ctx, e))
		case Upgrade:
			r.Status, r.ExitCode, r.Details = fill(StatusUpgraded, b.Upgrade(ctx, e))
		case Uninstall:
			r.Status, r.ExitCode, r.Details = fill(StatusRemoved, b.Uninstall(ctx, e))
		}
		if action != None {
			acted = true
		}
		reports = append(reports, r)
	}
	if acted {
		// Re-read rather than trust the exit code: the version the host
		// has now is the report's point.
		if after, err := b.Installed(ctx); err == nil {
			installed = after
		}
	}
	for i := range reports {
		reports[i].InstalledVersion = installed[strings.ToLower(reports[i].Entry.Package)]
	}
	return reports
}

// fill is an action's status unless the backend never ran it.
func fill(status string, r Result) (string, int, string) {
	if r.Status != "" {
		status = r.Status
	}
	return status, r.ExitCode, r.Details
}
