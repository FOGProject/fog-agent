// Package software is the software capability (design 0003): a desired set
// of packages the host is converged toward through a package manager,
// with detection done by the manager's own installed list. Chocolatey is
// the first Backend, then flatpak and snap; the convergence logic knows
// nothing about any of them.
package software

import (
	"context"
	"fmt"
	"sort"
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
	// StopSnapins: when this entry does not succeed, the host's snapin queue
	// waits. An older server omits it, which reads false.
	StopSnapins bool `json:"stop_snapins"`
}

// Policy is the software block of the desired state.
type Policy struct {
	// DriftInterval is how often, in seconds, the host re-checks the set
	// without the revision having moved.
	DriftInterval int `json:"drift_interval"`
	// Bootstrap is what to do when the backend is missing.
	Bootstrap Bootstrap `json:"bootstrap"`
	Entries   []Entry   `json:"entries"`
	// Manage is the level: "exclusive" also removes every package a backend
	// lists that is neither assigned nor protected. Anything else, an older
	// server's missing field included, means assigned only.
	Manage string `json:"manage,omitempty"`
	// Retain lists the packages of disabled entries. An exclusive host keeps
	// them instead of removing them. An older server omits it.
	Retain []RetainItem `json:"retain,omitempty"`
}

// RetainItem names a package an exclusive host must keep although no
// enabled entry assigns it, the package of a disabled entry.
type RetainItem struct {
	Backend string `json:"backend"`
	Package string `json:"package"`
}

// The levels the server sends in Policy.Manage.
const (
	ManageAssigned  = "assigned"
	ManageExclusive = "exclusive"
)

// ExclusiveTimeout is the timeout, in seconds, of an exclusive removal. It
// matches the server's default entry timeout (swTimeout 900), since a
// removal has no entry of its own to carry one.
const ExclusiveTimeout = 900

// stateRetained marks an Entry built from a retain item. It is internal: it
// never comes from the server and is not a state a backend is asked to hold.
const stateRetained = "retain"

// BackendNames is every backend name this agent has, in the order a
// backend-less exclusive pass visits them. BackendFor resolves each one.
func BackendNames() []string {
	return []string{"choco", "flatpak", "snap"}
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
	// before is the version installed when the run started.
	before string
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
		r := Report{Entry: e, Action: action, Status: StatusConverged, Details: why, before: installed[strings.ToLower(e.Package)]}
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
		r := &reports[i]
		r.InstalledVersion = installed[strings.ToLower(r.Entry.Package)]
		// An upgrade that found nothing newer changed nothing: report
		// it as converged, the truth, not as an upgrade. The exit code
		// still travels so a non-zero one is classified as usual.
		if r.Action == Upgrade && r.Status == StatusUpgraded && r.ExitCode == 0 && r.InstalledVersion == r.before {
			r.Status = StatusConverged
			r.Details = fmt.Sprintf("%s %s is the latest available\n%s", r.Entry.Package, r.before, r.Details)
		}
	}
	return reports
}

// Keeper is the optional half of a Backend that knows which installed
// packages are plumbing the backend or an assigned package needs (a
// runtime, a base, a dependency). A backend without it protects nothing
// beyond the assigned entries.
type Keeper interface {
	// Keep returns the lowercase ids that exclusive removal must never
	// touch. order, which may be nil, is the preferred removal order for
	// ids. An error means the protected set is unknown, and nothing is
	// removed.
	Keep(ctx context.Context, assigned []Entry, installed map[string]string) (keep map[string]bool, order []string, err error)
}

// Unwanted returns the installed ids exclusive removal should uninstall, in
// sorted order. It is nil for every level but exclusive.
//
// An id is wanted when ANY entry for the backend names it, present or
// absent: an absent entry is still managed by its entry, which already
// removed it or is about to. A blank id is never returned: there would be
// nothing to pass to the backend, and a remove-everything bug is exactly
// the shape this must not have.
func Unwanted(manage string, entries []Entry, installed map[string]string, keep map[string]bool) []string {
	if manage != ManageExclusive {
		return nil
	}
	assigned := make(map[string]bool, len(entries))
	for _, e := range entries {
		assigned[strings.ToLower(e.Package)] = true
	}
	var out []string
	for id := range installed {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || assigned[id] || keep[id] {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ConvergeExclusive is the exclusive level's removal step for one backend.
// Call it after Converge so the installed list it reads already holds what
// the assigned entries did. It returns the ids it asked the backend to
// remove. It acts only when the backend is available, the list reads, and
// the protected set is known: any doubt removes nothing. Doubt includes a
// Converge report of cannot_run for the backend (reports), and an assigned
// present entry that is still not installed after the pass: a failed install
// can leave dependencies behind that must not be mistaken for strays. Each
// removal is logged through logf and is not reported to the server, which
// has no row for a package it never assigned.
//
// The policy supplies the level and the retain list: packages of disabled
// entries, which an exclusive host keeps rather than removes.
func ConvergeExclusive(ctx context.Context, name string, b Backend, entries []Entry, reports []Report, policy Policy, logf func(string)) []string {
	if policy.Manage != ManageExclusive {
		return nil
	}
	manage := policy.Manage
	if ok, _ := b.Available(); !ok {
		return nil
	}
	for _, r := range reports {
		if r.Status == StatusCannotRun {
			logf(fmt.Sprintf("software %s: exclusive removal skipped, %q could not run", name, r.Entry.Package))
			return nil
		}
	}
	installed, err := b.Installed(ctx)
	if err != nil {
		logf(fmt.Sprintf("software %s: exclusive removal skipped, installed list: %s", name, err))
		return nil
	}
	for _, e := range entries {
		if e.State == StateAbsent {
			continue
		}
		if _, there := installed[strings.ToLower(e.Package)]; !there {
			logf(fmt.Sprintf("software %s: exclusive removal skipped, assigned %q is not installed", name, e.Package))
			return nil
		}
	}
	// A retained package is managed like an assigned one for protection
	// (Unwanted skips it, Keep follows its dependencies), but it is not an
	// entry the pass above had to install.
	managed := append([]Entry(nil), entries...)
	for _, r := range policy.Retain {
		if strings.EqualFold(r.Backend, name) {
			managed = append(managed, Entry{Backend: name, Package: r.Package, State: stateRetained})
		}
	}
	entries = managed
	var keep map[string]bool
	var order []string
	if k, ok := b.(Keeper); ok {
		keep, order, err = k.Keep(ctx, entries, installed)
		if err != nil {
			logf(fmt.Sprintf("software %s: exclusive removal skipped, protected set unknown: %s", name, err))
			return nil
		}
	}
	unwanted := Unwanted(manage, entries, installed, keep)
	var removed []string
	for _, id := range orderRemovals(unwanted, order) {
		r := b.Uninstall(ctx, Entry{Backend: name, Package: id, State: StateAbsent, Timeout: ExclusiveTimeout})
		_, code, detail := fill(StatusRemoved, r)
		logf(fmt.Sprintf("software %s: exclusive removal of %q, exit %d (%s)", name, id, code, firstLine(detail)))
		removed = append(removed, id)
	}
	return removed
}

// orderRemovals puts ids in the preferred order: those named in order first,
// in that order, then the rest as given. An id in order that is not in ids
// is ignored, so a preference can never add a removal.
func orderRemovals(ids, order []string) []string {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range order {
		if want[id] {
			out = append(out, id)
			delete(want, id)
		}
	}
	for _, id := range ids {
		if want[id] {
			out = append(out, id)
		}
	}
	return out
}

// IsPinned says whether the entry asks for an exact version.
func IsPinned(e Entry) bool {
	return e.Version != "" && e.Version != VersionLatest
}

// refusePin is what a backend that cannot hold an exact version answers to
// a pinned Install. The server refuses the pin at save; this covers old data.
func refusePin(name string, e Entry) Result {
	return Result{
		Status:  StatusCannotRun,
		Details: fmt.Sprintf("%s cannot hold an exact version (%s); set the version policy to any or latest", name, e.Version),
	}
}

// BackendFor returns the backend the server named, or nil when this agent
// has no such backend.
func BackendFor(name string) Backend {
	switch name {
	case "choco":
		return &Choco{}
	case "flatpak":
		return &Flatpak{}
	case "snap":
		return &Snap{}
	}
	return nil
}

// Group splits the entries by backend. The names come back in order of
// first appearance, and each group keeps the entries' own order.
func Group(entries []Entry) (names []string, groups map[string][]Entry) {
	groups = map[string][]Entry{}
	for _, e := range entries {
		if _, seen := groups[e.Backend]; !seen {
			names = append(names, e.Backend)
		}
		groups[e.Backend] = append(groups[e.Backend], e)
	}
	return names, groups
}

// fill is an action's status unless the backend never ran it.
func fill(status string, r Result) (string, int, string) {
	if r.Status != "" {
		status = r.Status
	}
	return status, r.ExitCode, r.Details
}
