// Package directory reports what directory the machine is actually a member
// of, and where its computer object actually sits (design 0009). These are
// facts, not desired state: they ride the poll request when their hash
// changes, exactly like inventory, and nothing here joins, leaves or moves
// anything.
//
// The distinction the package exists to make is design 0009 §2's: membership
// is something only the machine can change, placement is something only the
// directory can. This half reports both so the server can tell them apart --
// FOG has never recorded either, only what an admin asked for.
//
// Follows 0006's split exactly: a neutral file holding the type and the
// shared logic, one gather_<goos>.go per platform, and gather_other.go
// answering (zero, false) everywhere else.
package directory

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// The kinds of membership this reports. A machine is in exactly one.
const (
	// KindAD is a classic on-premises directory: an AD domain joined with
	// NetJoinDomain, adcli, realmd or net ads. The only kind design 0009 §5
	// can move, because it is the only one with an LDAP object to rename.
	KindAD = "ad"
	// KindEntra is Entra ID (Azure AD) joined. Reported so an Entra machine
	// is not mis-filed as unjoined; joining one is out of scope (0009 §9).
	KindEntra = "entra"
	// KindWorkgroup is a Windows machine in a workgroup -- a positive answer
	// meaning "asked, and it is in none", not a failure to look.
	KindWorkgroup = "workgroup"
	// KindNone is the same positive answer on a platform with no workgroup
	// concept.
	KindNone = "none"
)

// Directory is what the machine says about its own membership.
//
// Every field is always sent (no omitempty), for 0006's reason: the block is
// the whole row, so an unknown field is an explicit empty string and the
// server writes a full row rather than merging against a stale one. There is
// deliberately no timestamp here -- see design 0009 §3.
type Directory struct {
	// Joined is membership of a real directory. False with a Kind of
	// workgroup or none is an answer; a collector that could not tell
	// returns ok=false from Gather and sends nothing at all.
	Joined bool `json:"joined"`
	// Kind is one of the constants above.
	Kind string `json:"kind"`
	// Domain is the DNS domain name, lowercased: corp.example.com.
	Domain string `json:"domain"`
	// Netbios is the short domain name, uppercased: CORP. Kept separate
	// because FOG's hostADDomain has always held whichever one the admin
	// typed, and telling them apart is the only way to compare honestly.
	Netbios string `json:"netbios"`
	// ComputerDN is the full distinguished name of this machine's computer
	// object. The load-bearing field: it is what a server-side Modify DN
	// needs, and reporting it means the server never has to search the
	// directory by name and guess between duplicates.
	//
	// Empty is normal and not an error -- no Linux join tool exposes it, so
	// the server falls back to searching for MachineAccount.
	ComputerDN string `json:"computer_dn"`
	// MachineAccount is the account name with its trailing dollar: WS-014$.
	MachineAccount string `json:"machine_account"`
	// Site is the directory site the machine resolved to, where the platform
	// can say. Reported because "joined, but to a domain controller across
	// the WAN" is a real condition an estate owner wants to see.
	Site string `json:"site"`
}

// Gather collects the machine's membership. The bool is "a collector ran
// here", never "the machine is joined": a platform with no implementation,
// or one where every probe failed, returns false and the block is omitted
// from the poll entirely. Sending a zero Directory instead would tell the
// server the machine had left its domain (design 0006 §6's rule, and here it
// would put a whole estate into drift on one bad release).
func Gather() (Directory, bool) {
	d, ok := gather()
	if !ok {
		return Directory{}, false
	}
	return d.normalize(), true
}

// normalize puts the case conventions the comparison depends on onto the
// fields, in one place. The server compares the reported domain against
// hostADDomain, which holds whatever an admin typed into a form; doing the
// folding here means the comparison itself stays a plain equality.
func (d Directory) normalize() Directory {
	d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
	d.Domain = strings.ToLower(strings.TrimSpace(d.Domain))
	d.Netbios = strings.ToUpper(strings.TrimSpace(d.Netbios))
	d.ComputerDN = strings.TrimSpace(d.ComputerDN)
	d.Site = strings.TrimSpace(d.Site)

	d.MachineAccount = strings.TrimSpace(d.MachineAccount)
	// A machine account name ends in a dollar. Some sources give it and
	// some do not, and a server comparing "WS-014" against "WS-014$" would
	// see drift that is not there.
	if d.MachineAccount != "" && !strings.HasSuffix(d.MachineAccount, "$") {
		d.MachineAccount += "$"
	}

	if !d.Joined {
		// Not joined means none of the rest can be true. A stale domain
		// left on an unjoined machine is exactly the kind of half-answer
		// that makes a drift report untrustworthy.
		d.Domain = ""
		d.Netbios = ""
		d.ComputerDN = ""
		d.MachineAccount = ""
		d.Site = ""
		if d.Kind == "" {
			d.Kind = KindNone
		}
	}
	return d
}

// Hash is the resend gate, same construction as inventory's.
func (d Directory) Hash() string {
	b, err := json.Marshal(d)
	if err != nil {
		// A struct of strings and a bool does not fail to marshal; if it
		// somehow did, a changing hash forces a resend rather than hiding
		// the fault.
		return fmt.Sprintf("unmarshalable-%v", err)
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}
