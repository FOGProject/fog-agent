package main

import (
	"fmt"
	"time"

	"github.com/FOGProject/fog-agent/internal/directory"
	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/inventory"
	"github.com/FOGProject/fog-agent/internal/network"
	"github.com/FOGProject/fog-agent/internal/printers"
	"github.com/FOGProject/fog-agent/internal/software"
)

// FactsInterval is how often the collectors re-run. Facts move on the scale
// of someone installing a program or swapping a disk, not on the scale of a
// poll, and enumerating a package-managed host's packages is the most
// expensive thing the agent does. The server can always ask sooner with
// want_inventory / want_software.
const FactsInterval = time.Hour

// factsDue reports whether the collectors should run this poll: the server
// asked, or the interval elapsed. A zero FactsChecked -- a fresh agent --
// is due, which is what makes the first poll after enrollment carry facts.
func factsDue(cfg enroll.Config, now time.Time) bool {
	if cfg.FactsDisabled {
		// The site turned collection off. Not gathering is the point: the
		// server would ignore the block anyway, so running the collectors
		// would be pure cost on every host in the estate.
		return false
	}
	return cfg.WantInventory || cfg.WantSoftware || cfg.WantDirectory ||
		cfg.WantPrinters || now.Sub(cfg.FactsChecked) >= FactsInterval
}

// sentFacts is the hashes of the blocks a poll actually carried, empty for
// a block that was not sent. The caller stores them only after the poll
// succeeds (see recordFacts).
type sentFacts struct {
	gathered  bool // the collectors ran this poll
	inventory string
	software  string
	directory string
	printers  string
	network   string
}

// attachFacts runs the collectors when due and hangs onto the request only
// the blocks the server does not already have. A collector that could not
// run reports nothing at all rather than an empty result: the server treats
// a reported software list as complete and marks everything absent from it
// as removed, so a false empty would wipe a host's history (design 0006 §6).
func attachFacts(st *enroll.State, req *enroll.PollRequest, now time.Time, out *sayer) sentFacts {
	var sent sentFacts
	if st.Config.FactsDisabled {
		return sent
	}
	// The interfaces are gathered on EVERY poll, not on FactsInterval.
	// The others are expensive -- enumerating a package-managed host is
	// the most costly thing this agent does -- and this one is a single
	// syscall the runtime already caches nothing for. The accuracy is
	// worth it: this is the fact the wake relay picks senders from, so an
	// hour of staleness is an hour of the server asking a laptop that has
	// gone home to broadcast on a subnet it left (design 0011 section 3).
	// It still only goes on the wire when the hash moves.
	if n, ok := network.Gather(); ok {
		if h := n.Hash(); h != st.Config.NetworkHash || st.Config.WantNetwork {
			req.Network = &n
			sent.network = h
		}
	}

	if !factsDue(st.Config, now) {
		return sent
	}
	sent.gathered = true
	if inv, ok := inventory.Gather(); ok {
		if h := inv.Hash(); h != st.Config.InventoryHash || st.Config.WantInventory {
			req.Inventory = &inv
			sent.inventory = h
		}
	}
	if progs, ok := software.List(); ok {
		if h := software.Hash(progs); h != st.Config.SoftwareHash || st.Config.WantSoftware {
			req.Software = progs
			sent.software = h
		}
	}
	if dir, ok := directory.Gather(); ok {
		if h := dir.Hash(); h != st.Config.DirectoryHash || st.Config.WantDirectory {
			req.Directory = &dir
			sent.directory = h
		}
	}
	if pr, ok := printers.Gather(); ok {
		if h := pr.Hash(); h != st.Config.PrintersHash || st.Config.WantPrinters {
			req.Printers = &pr
			sent.printers = h
		}
	}
	if sent.inventory != "" || sent.software != "" || sent.directory != "" ||
		sent.printers != "" || sent.network != "" {
		queues := 0
		if req.Printers != nil {
			queues = len(req.Printers.Installed)
		}
		links := 0
		if req.Network != nil {
			links = len(req.Network.Interfaces)
		}
		out.say(fmt.Sprintf(
			"facts: sending inventory=%t software=%d directory=%t printers=%d network=%d",
			req.Inventory != nil, len(req.Software), req.Directory != nil,
			queues, links))
	}
	return sent
}

// recordFacts stores what the server accepted and what it is now asking
// for. Called only on a successful poll: a failed one leaves both the
// hashes and FactsChecked alone, so the very next poll gathers and resends
// rather than waiting out the interval on facts the server never got.
func recordFacts(st *enroll.State, sent sentFacts, resp *enroll.PollResponse, now time.Time) error {
	// Config holds slices, so it is not comparable; the fields this
	// function owns are, and they are the only ones that can have moved.
	type owned struct {
		inv, soft, dir, print, net              string
		checked                                 time.Time
		wantI, wantS, wantD, wantP, wantN, offX bool
	}
	// One snapshot function rather than two literals: the pair only means
	// anything if both sides list the same fields in the same order, and a
	// field added to one and not the other silently stops persisting.
	snapshot := func() owned {
		return owned{st.Config.InventoryHash, st.Config.SoftwareHash,
			st.Config.DirectoryHash, st.Config.PrintersHash, st.Config.NetworkHash,
			st.Config.FactsChecked,
			st.Config.WantInventory, st.Config.WantSoftware,
			st.Config.WantDirectory, st.Config.WantPrinters, st.Config.WantNetwork,
			st.Config.FactsDisabled}
	}
	before := snapshot()

	if sent.gathered {
		st.Config.FactsChecked = now
	}
	if sent.inventory != "" {
		st.Config.InventoryHash = sent.inventory
	}
	if sent.software != "" {
		st.Config.SoftwareHash = sent.software
	}
	if sent.directory != "" {
		st.Config.DirectoryHash = sent.directory
	}
	if sent.printers != "" {
		st.Config.PrintersHash = sent.printers
	}
	if sent.network != "" {
		st.Config.NetworkHash = sent.network
	}
	// The answer is authoritative: the server stops asking once it holds
	// a hash, so assigning rather than or-ing is what clears the flags.
	st.Config.WantInventory = resp.WantInventory
	st.Config.WantSoftware = resp.WantSoftware
	st.Config.WantDirectory = resp.WantDirectory
	st.Config.WantPrinters = resp.WantPrinters
	st.Config.WantNetwork = resp.WantNetwork
	// Absent leaves the setting alone: a server too old to send the gate
	// must not read as one that turned it off.
	if resp.CollectFacts != nil {
		st.Config.FactsDisabled = !*resp.CollectFacts
	}

	after := snapshot()
	if before == after {
		return nil
	}
	return st.SaveConfig()
}

// UnauthorizedGrace is how long a 401 that does not name this agent's
// certificate must persist before the agent gives up and re-enrolls.
//
// It exists only for the 401 that never reaches the application at all --
// a TLS-layer rejection, or a proxy answering on the server's behalf --
// where there is no body to carry a reason and so no way to tell a real
// revocation from an outage. When the server CAN answer, it says
// `unknown_certificate` and the agent acts at once, so nothing legitimate
// waits this long.
//
// A week, because the cost is asymmetric. Waiting too long leaves one host
// unmanaged; re-enrolling too eagerly puts every host in the estate into a
// queue an admin has to clear by hand, which is exactly the failure this
// was written for. A weekend outage must not be enough.
const UnauthorizedGrace = 7 * 24 * time.Hour

// unauthorizedTooLong records when the run of refusals started and reports
// whether it has now outlasted the grace period.
func unauthorizedTooLong(st *enroll.State, now time.Time) (bool, error) {
	if st.Config.UnauthorizedSince.IsZero() {
		st.Config.UnauthorizedSince = now
		if err := st.SaveConfig(); err != nil {
			return false, err
		}
	}
	return now.Sub(st.Config.UnauthorizedSince) >= UnauthorizedGrace, nil
}

// unauthorizedFor renders how long the refusals have been going on, for
// the log line that is the only warning anyone gets before the grace
// period expires.
func unauthorizedFor(st *enroll.State, now time.Time) string {
	if st.Config.UnauthorizedSince.IsZero() {
		return "0s"
	}
	return now.Sub(st.Config.UnauthorizedSince).Round(time.Second).String()
}

// clearUnauthorized forgets the run of refusals after a poll succeeds, so
// an outage followed by a recovery does not accumulate toward the grace
// period across unrelated incidents.
func clearUnauthorized(st *enroll.State) error {
	if st.Config.UnauthorizedSince.IsZero() {
		return nil
	}
	st.Config.UnauthorizedSince = time.Time{}
	return st.SaveConfig()
}
