package printerset

import (
	"context"
	"os/exec"
	"strings"
)

// Timeout bounds one lpadmin or PowerShell call. A spooler that has wedged
// on an unreachable device must not hold the poll open.
const Timeout = 60

// CUPS drives lpadmin, on Linux and macOS.
//
// The three corrections to the legacy client's UnixPrinterManager, each one
// a thing that has never worked:
//
//   - Remove runs `lpadmin -x`. The legacy client ran `lpstat - -P {name}`,
//     which is a STATUS QUERY -- it cannot remove a printer and never
//     could, so it discarded the output and returned as if it had. Mode 2's
//     entire content is removal, so that mode has never removed anything on
//     Linux and has reported success every poll since.
//   - SetDefault exists. Default() threw NotImplementedException, so
//     paIsDefault has never been honored on Linux.
//   - The queue name is not computed by shelling out to `echo ... | tr`.
type CUPS struct{}

// Available reports whether lpadmin is here.
func (CUPS) Available() (bool, string) {
	if _, err := exec.LookPath("lpadmin"); err != nil {
		return false, "lpadmin is not installed; CUPS is required to manage printers"
	}
	return true, ""
}

// Add installs a queue, or reconfigures one that exists -- lpadmin is an
// upsert, so the same call serves both.
func (CUPS) Add(ctx context.Context, p Printer) Result {
	args := []string{"-p", p.Name, "-E", "-v", p.URI}
	args = append(args, driverArgs(p.Driver, p.URI)...)
	if out, err := run(ctx, "lpadmin", args...); err != nil {
		return Result{Status: StatusFailed, Error: message(out, err)}
	}
	return Result{Status: StatusInstalled}
}

// Remove deletes a queue.
func (CUPS) Remove(ctx context.Context, name string) Result {
	if out, err := run(ctx, "lpadmin", "-x", name); err != nil {
		return Result{Status: StatusFailed, Error: message(out, err)}
	}
	return Result{Status: StatusRemoved}
}

// SetDefault makes a queue the system default.
//
// `lpadmin -d`, not `lpoptions -d`: lpoptions writes a per-user default into
// that user's ~/.cups/lpoptions, and the agent runs as root -- so it would
// set the default for nobody who prints.
func (CUPS) SetDefault(ctx context.Context, name string) Result {
	if out, err := run(ctx, "lpadmin", "-d", name); err != nil {
		return Result{Status: StatusFailed, Error: message(out, err)}
	}
	return Result{Status: StatusUpdated}
}

// driverArgs turns a driver string into lpadmin's arguments, which depends
// on the device URI as well.
//
// The empty driver is the interesting one, and it means two different things
// depending on where the queue points:
//
//   - Over IPP it means DRIVERLESS: the printer describes its own
//     capabilities and CUPS spells that `-m everywhere`. FOG's model has
//     never been able to express this at all, because pDefFile and pModel
//     are the only way to say what to print with and neither can say
//     "neither".
//   - Over anything else it means a RAW queue: no PPD, jobs passed through
//     untouched. That is what a FOG `Local` or `Network` printer with no
//     model has always been, and lpadmin makes one when given no -m at all.
//
// Sending `-m everywhere` at a socket:// device is not merely wrong, it is
// refused: lpadmin answers "IPP Everywhere driver requires an IPP
// connection" and the queue is not created. Seen in the lab on 2026-09-04
// on the first real end-to-end run, against a printer defined the oldest
// way FOG allows -- which is to say the common case.
func driverArgs(driver, uri string) []string {
	driver = strings.TrimSpace(driver)
	if driver == "" {
		if ippTransport(uri) {
			return []string{"-m", "everywhere"}
		}
		return nil
	}
	// A path to a PPD file goes in with -P; anything else is a model name
	// lpadmin looks up with -m.
	if strings.HasSuffix(strings.ToLower(driver), ".ppd") ||
		strings.HasSuffix(strings.ToLower(driver), ".ppd.gz") {
		return []string{"-P", driver}
	}
	return []string{"-m", driver}
}

// ippTransport says whether a device URI speaks IPP, which is what the
// everywhere driver needs. https:// counts: CUPS treats it as IPP over TLS.
func ippTransport(uri string) bool {
	scheme := strings.ToLower(strings.TrimSpace(uri))
	if i := strings.Index(scheme, ":"); i > 0 {
		scheme = scheme[:i]
	}
	switch scheme {
	case "ipp", "ipps", "http", "https":
		return true
	}
	return false
}

// run executes a command with the timeout, returning its combined output.
func run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// message is the line an admin sees in the report: the tool's own words when
// it had any, else the exit error.
func message(out string, err error) string {
	out = strings.TrimSpace(out)
	if out != "" {
		// The first line. lpadmin says "lpadmin: Bad device-URI scheme" and
		// then, sometimes, a paragraph about usage.
		if i := strings.IndexByte(out, '\n'); i > 0 {
			out = out[:i]
		}
		return out
	}
	if err != nil {
		return err.Error()
	}
	return ""
}
