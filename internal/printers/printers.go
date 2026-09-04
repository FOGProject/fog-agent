// Package printers reports the printers actually installed on the machine
// (design 0010 §3). Facts, not desired state: they ride the poll request when
// their hash changes, exactly like inventory and directory, and nothing here
// adds, removes or reconfigures anything.
//
// The gap this closes is design 0010 §1.4. Both legacy platform managers had
// a GetPrinters(), and both used it only to decide what to strip in mode 2 --
// all three call sites are local decisions in PrinterManager.cs and that file
// contains no transmit. So FOG has never been able to answer "did the printer
// I assigned actually install?", which is the question every printer support
// call starts with.
//
// A printer is described the way both spoolers already describe one: a device
// URI and a driver (design 0010 §2). CUPS takes a device URI directly; a
// Windows TCP/IP port is the same information written differently, so the
// Windows collector reconstructs the URI rather than reporting a port name
// that means nothing off that machine.
//
// Follows 0006's split: a neutral file holding the type and the shared logic,
// one gather_<goos>.go per platform, and gather_other.go answering
// (zero, false) everywhere else.
package printers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The print subsystem the machine runs. This is the honest home for the fact
// FOG's pConfig column was trying to carry -- reported by the machine that
// knows, rather than chosen from a dropdown by an admin who has to guess
// (design 0010 §1.1).
const (
	// SubsystemCUPS is CUPS, on Linux and on macOS.
	SubsystemCUPS = "cups"
	// SubsystemWinspool is the Windows print spooler.
	SubsystemWinspool = "winspool"
)

// Printer is one installed print queue.
//
// Every field is always sent (no omitempty), for 0006's reason: the block is
// the whole row, so an unknown field is an explicit empty rather than a key
// the server has to merge against something stale.
type Printer struct {
	// Name is the queue name as the user sees it.
	Name string `json:"name"`
	// URI is how this machine reaches the device: socket://, ipp://, ipps://,
	// lpd://, smb://, usb://. Empty when the spooler named a port that
	// carries no addressing information at all -- see opaqueURI.
	URI string `json:"uri"`
	// Driver is the print driver or PPD model. Empty is a real answer and
	// means driverless (IPP Everywhere), which design 0010 §2 makes a
	// first-class case because FOG's model has never been able to express it.
	Driver string `json:"driver"`
	// Shared is whether this machine re-shares the queue to others.
	Shared bool `json:"shared"`
}

// Printers is the whole block.
type Printers struct {
	// Subsystem is SubsystemCUPS or SubsystemWinspool.
	Subsystem string `json:"subsystem"`
	// Default is the name of the machine's default printer, empty when it
	// has none. Reported because printerAssoc.paIsDefault has always said
	// which one it should be and nothing has ever checked.
	Default string `json:"default"`
	// Installed is every queue on the machine, sorted by name. Never nil:
	// an empty list is the positive answer "this machine has no printers",
	// and it is meaningfully different from the block being absent.
	Installed []Printer `json:"installed"`
}

// Gather collects the machine's printers. The bool is "a collector ran here",
// never "the machine has printers".
//
// The distinction is load-bearing and it is the same trap design 0006 §6
// names. The server treats a reported list as complete, so a machine with no
// spooler at all must send nothing rather than an empty list -- an empty list
// says "I asked the spooler and it has none", which would wipe the host's
// printer history on every Linux box that does not have CUPS installed.
func Gather() (Printers, bool) {
	p, ok := gather()
	if !ok {
		return Printers{}, false
	}
	return p.normalize(), true
}

// normalize sorts, trims, de-duplicates and canonicalizes, so that two polls
// from an unchanged machine hash the same.
//
// Sorting is not cosmetic. Neither EnumPrinters nor lpstat promises an order,
// and an unordered list would move the hash on a machine where nothing
// changed -- which would resend the block every poll and defeat the whole
// gate.
func (p Printers) normalize() Printers {
	p.Subsystem = strings.ToLower(strings.TrimSpace(p.Subsystem))
	p.Default = strings.TrimSpace(p.Default)

	out := make([]Printer, 0, len(p.Installed))
	seen := make(map[string]bool, len(p.Installed))
	for _, q := range p.Installed {
		q.Name = strings.TrimSpace(q.Name)
		if q.Name == "" {
			// A queue with no name is not addressable by anything -- not the
			// report, not a removal, not the admin. Dropping it is better
			// than carrying a row nothing can act on.
			continue
		}
		if seen[q.Name] {
			continue
		}
		seen[q.Name] = true
		q.URI = normalizeURI(q.URI)
		q.Driver = strings.TrimSpace(q.Driver)
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	p.Installed = out

	// A default naming a queue that is not installed is not a default. It
	// happens after a removal that did not clear the setting, and reporting
	// it would put the host into a drift that no action can resolve.
	if p.Default != "" && !seen[p.Default] {
		p.Default = ""
	}
	return p
}

// normalizeURI lowercases the scheme and nothing else.
//
// Only the scheme. A queue path is case-sensitive on the far end --
// smb://srv/HP4550 and smb://srv/hp4550 are not the same share on a
// case-sensitive server -- so folding the whole URI would turn a comparison
// that should be exact into one that sometimes lies.
func normalizeURI(uri string) string {
	uri = strings.TrimSpace(uri)
	i := strings.Index(uri, ":")
	if i <= 0 {
		return uri
	}
	return strings.ToLower(uri[:i]) + uri[i:]
}

// opaqueURI wraps a port name the collector could not express as a device
// URI -- a USB00x port, FILE:, PORTPROMPT:, a third-party port monitor.
//
// Reported rather than blanked, and reported in a form that cannot be
// mistaken for an address: the admin needs to see that the queue exists, and
// an invented socket:// URI for a port whose host nobody knows would be worse
// than saying plainly that this one has no URI.
func opaqueURI(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		return ""
	}
	return "port:" + port
}

// Hash is the resend gate, same construction as inventory's and directory's.
func (p Printers) Hash() string {
	b, err := json.Marshal(p)
	if err != nil {
		// Strings, bools and a slice of the same do not fail to marshal; if
		// this somehow did, a changing hash forces a resend rather than
		// hiding the fault.
		return fmt.Sprintf("unmarshalable-%v", err)
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}
