// Package printerset is the printers capability (design 0010 §5): a desired
// set of print queues the host is converged toward, described as a device URI
// and a driver, with an outcome reported per printer.
//
// The shape follows the software provider: a Backend the convergence logic
// knows nothing about, and a pure Decide so every row of the rule is a test
// case.
//
// What it replaces is worth naming, because two of the three legacy modes
// have never worked on Linux at all. UnixPrinterManager.Remove() runs
// `lpstat - -P {name}` -- lpstat is CUPS' status QUERY tool and has never
// been able to remove a printer, removal is `lpadmin -x` -- so mode 2, whose
// entire content is removal, has reported success every poll while removing
// nothing. Default() and Configure() throw outright, so paIsDefault has never
// been honored there either.
package printerset

import (
	"context"
	"strings"

	facts "github.com/FOGProject/fog-agent/internal/printers"
)

// The modes, in words. The server sends these; hostPrinterLevel stores 0, 1
// or 2 and the legacy wire sends 0, `a` or `ar` -- two vocabularies for one
// setting, neither of them written down anywhere an admin can see (design
// 0010 §1.3). This is the third and the only one that says what it means.
const (
	// ManageOff leaves printers alone entirely.
	ManageOff = "off"
	// ManageAssigned installs and maintains the assigned printers; anything
	// else on the machine is left alone.
	ManageAssigned = "assigned"
	// ManageExclusive is ManageAssigned plus removing what FOG did not
	// assign. It takes a printer off somebody's workstation, so it has to
	// work correctly or not at all.
	ManageExclusive = "exclusive"
)

// What the agent reports for one printer. These are the server's
// PrinterSet::STATUSES; the first four settle the row and clear any error
// recorded against it.
const (
	StatusConverged   = "converged"
	StatusInstalled   = "installed"
	StatusUpdated     = "updated"
	StatusRemoved     = "removed"
	StatusFailed      = "failed"
	StatusUnsupported = "unsupported"
)

// MaxError is how much of a backend's message is worth sending. The server
// column is a varchar(255) and this is a line an admin reads in a report,
// not a log.
const MaxError = 255

// Printer is one queue the server wants on this host.
type Printer struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// URI is the device URI: socket://, ipp://, ipps://, lpd://, smb://.
	// Empty means the server could neither derive nor be given one, which
	// is a configuration gap and reported as such rather than guessed at.
	URI string `json:"uri"`
	// Driver is the print driver or PPD model. Empty means driverless
	// (IPP Everywhere), which is a value and not a missing field.
	Driver string `json:"driver"`
}

// Policy is the printers block of the desired state.
type Policy struct {
	Manage string `json:"manage"`
	// Default is the queue name that should be the machine's default,
	// empty for "no opinion".
	Default  string    `json:"default"`
	Printers []Printer `json:"printers"`
}

// Action is what convergence decided for one printer.
type Action int

// The actions.
const (
	None Action = iota
	Add
	Update
	Remove
)

func (a Action) String() string {
	switch a {
	case Add:
		return "add"
	case Update:
		return "update"
	case Remove:
		return "remove"
	}
	return "none"
}

// Result is what a backend action came back with.
type Result struct {
	Status string
	Error  string
}

// Backend installs, removes and defaults queues on one print subsystem.
//
// Reading is deliberately NOT on this interface: the observed set comes from
// internal/printers, the same collector that reports the facts. One reader
// means the convergence and the report can never disagree about what is on
// the machine, which is the disagreement that would make both untrustworthy.
type Backend interface {
	// Available says whether this backend can run here at all; the detail
	// names what is missing when it cannot.
	Available() (bool, string)
	// Add installs a queue, or reconfigures one that exists -- both
	// spoolers treat the operation as an upsert.
	Add(ctx context.Context, p Printer) Result
	// Remove deletes a queue.
	Remove(ctx context.Context, name string) Result
	// SetDefault makes a queue the machine's default.
	SetDefault(ctx context.Context, name string) Result
}

// Decide is the convergence rule for one wanted printer against what is
// installed. Pure, so every row of the rule is a test case.
//
// THE DRIVER IS NOT COMPARED, and that is the subtle part. The driver string
// the collector observes comes from the spooler's own vocabulary --
// `printer-make-and-model` on CUPS, which reads "Canon MF650C Series UFR II"
// -- and it is not the string that was passed to `lpadmin -m`, which is a
// model URI or a PPD path. Comparing them would find a difference on every
// poll, order an update that changes nothing, and find the same difference
// again forever. That is the same self-inflicted hot loop the 401 branch of
// the run loop had, arrived at from a different direction, and it would run
// lpadmin against every printer in the estate every five minutes.
//
// So the URI is the identity: it is what the spooler reports back verbatim,
// and it is what actually determines where the job goes. The driver is
// applied when the queue is created and left alone after.
func Decide(want Printer, installed map[string]facts.Printer) (Action, string) {
	if strings.TrimSpace(want.URI) == "" {
		// Not a failure of this machine. The server could neither derive a
		// URI from the printer's type and address nor be given one, and
		// installing a queue pointed at nothing would be worse than saying
		// so: the message lands in the report against that printer.
		return None, "no device URI: set one on the printer, or give it a type and address to derive from"
	}
	have, present := installed[want.Name]
	if !present {
		return Add, want.Name + " is not installed"
	}
	if !sameURI(have.URI, want.URI) {
		return Update, want.Name + " points at " + have.URI + ", want " + want.URI
	}
	return None, want.Name + " is installed at " + have.URI
}

// sameURI compares two device URIs.
//
// The scheme is case-insensitive and everything after it is not: a queue
// path is case-sensitive on the far end, so smb://srv/HP4550 and
// smb://srv/hp4550 are not the same share on a case-sensitive server.
func sameURI(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	ai, bi := strings.Index(a, ":"), strings.Index(b, ":")
	if ai <= 0 || bi <= 0 {
		return a == b
	}
	return strings.EqualFold(a[:ai], b[:bi]) && a[ai:] == b[bi:]
}

// Unwanted returns the installed queues that should be removed, in name
// order. Empty for every mode but exclusive.
//
// A queue with no name is never returned: there would be nothing to pass to
// the backend, and a remove-everything bug is exactly the shape this must
// not have.
func Unwanted(policy Policy, installed []facts.Printer) []string {
	if policy.Manage != ManageExclusive {
		return nil
	}
	want := make(map[string]bool, len(policy.Printers))
	for _, p := range policy.Printers {
		want[p.Name] = true
	}
	var out []string
	for _, q := range installed {
		// Trimmed, not just compared to "": the collector normalizes names
		// before they get here, but this function is the one that hands a
		// name to `lpadmin -x`, so it does its own checking.
		if strings.TrimSpace(q.Name) == "" || want[q.Name] {
			continue
		}
		out = append(out, q.Name)
	}
	return out
}

// Report is what happened to one printer, ready to send.
type Report struct {
	Printer Printer
	Status  string
	Error   string
	// Detail is the human line for the agent's own log; it is not sent.
	Detail string
}

// Unsupported reports every assigned printer as unsupported for one reason,
// for when nothing can be attempted at all: no lpadmin, or the installed set
// could not be read.
//
// It reports rather than falling silent because a silent printer is unknown
// to the server, and "unknown" is exactly the state design 0010 exists to end.
// An admin looking at the report should read "lpadmin is not installed"
// against the printer, not an empty cell -- the legacy client's answer to a
// host that could not install a printer was nothing at all.
//
// The reason is NOT retried in a tight loop by the caller: an absent lpadmin
// is an admin's next move, not this poll's.
func Unsupported(policy Policy, reason string) []Report {
	if policy.Manage == ManageOff || policy.Manage == "" {
		return nil
	}
	reports := make([]Report, 0, len(policy.Printers))
	for _, want := range policy.Printers {
		reports = append(reports, Report{
			Printer: want, Status: StatusUnsupported,
			Error: truncate(reason), Detail: reason,
		})
	}
	return reports
}

// Converge brings the machine to the policy and reports on every assigned
// printer -- including the ones that needed nothing, because the server
// reads a converged report as "still true" and a silent printer as unknown.
//
// Removals in exclusive mode are performed but NOT reported per printer:
// they are queues FOG never assigned, so there is no row to hang a result
// on. They reach the server as the next facts report, where they are simply
// gone from the installed list.
func Converge(ctx context.Context, backend Backend, policy Policy, observed facts.Printers) []Report {
	if policy.Manage == ManageOff || policy.Manage == "" {
		return nil
	}

	byName := make(map[string]facts.Printer, len(observed.Installed))
	for _, q := range observed.Installed {
		byName[q.Name] = q
	}

	reports := make([]Report, 0, len(policy.Printers))
	for _, want := range policy.Printers {
		action, why := Decide(want, byName)
		switch action {
		case Add, Update:
			res := backend.Add(ctx, want)
			if res.Status == StatusInstalled && action == Update {
				res.Status = StatusUpdated
			}
			reports = append(reports, Report{
				Printer: want, Status: res.Status,
				Error: truncate(res.Error), Detail: why,
			})
		default:
			status := StatusConverged
			errText := ""
			if strings.TrimSpace(want.URI) == "" {
				// Decide's one non-action failure: nothing was attempted
				// because there was nothing to attempt it against.
				status = StatusFailed
				errText = truncate(why)
			}
			reports = append(reports, Report{
				Printer: want, Status: status,
				Error: errText, Detail: why,
			})
		}
	}

	for _, name := range Unwanted(policy, observed.Installed) {
		backend.Remove(ctx, name)
	}

	// The default last, so a queue installed a moment ago can be made it.
	if policy.Default != "" && observed.Default != policy.Default {
		backend.SetDefault(ctx, policy.Default)
	}

	return reports
}

// truncate keeps a backend message to what the server's column holds.
func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= MaxError {
		return s
	}
	return s[:MaxError]
}
