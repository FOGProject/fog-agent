package main

import (
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

func TestFactsDue(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		cfg  enroll.Config
		want bool
	}{
		{"fresh agent has never gathered", enroll.Config{}, true},
		{"just gathered", enroll.Config{FactsChecked: now.Add(-time.Minute)}, false},
		{"interval elapsed", enroll.Config{FactsChecked: now.Add(-2 * FactsInterval)}, true},
		// The server asking outranks the interval: a restored database
		// needs the block now, not up to an hour from now.
		{"server wants inventory", enroll.Config{FactsChecked: now, WantInventory: true}, true},
		{"server wants software", enroll.Config{FactsChecked: now, WantSoftware: true}, true},
		{"server wants directory", enroll.Config{FactsChecked: now, WantDirectory: true}, true},
		{"server wants printers", enroll.Config{FactsChecked: now, WantPrinters: true}, true},
		// The gate outranks everything, including a request the server
		// made before the setting changed: an agent that kept gathering
		// would burn CPU on every host for a block the server discards.
		{"collection disabled", enroll.Config{FactsDisabled: true}, false},
		{"disabled beats a pending request",
			enroll.Config{FactsDisabled: true, WantInventory: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := factsDue(tc.cfg, now); got != tc.want {
				t.Errorf("factsDue() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestRecordFactsStoresOnlyWhatWasSent(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	st.Config.SoftwareHash = "keepme"
	now := time.Now()

	// Inventory went up, software did not: the software hash must survive.
	sent := sentFacts{gathered: true, inventory: "invhash"}
	if err := recordFacts(st, sent, &enroll.PollResponse{}, now); err != nil {
		t.Fatal(err)
	}
	if st.Config.InventoryHash != "invhash" {
		t.Errorf("InventoryHash = %q, want invhash", st.Config.InventoryHash)
	}
	if st.Config.SoftwareHash != "keepme" {
		t.Errorf("SoftwareHash = %q, an unsent block must not be recorded", st.Config.SoftwareHash)
	}
	if !st.Config.FactsChecked.Equal(now) {
		t.Error("a gather must stamp FactsChecked, or the collectors run every poll")
	}
}

func TestRecordFactsKeepsTheThreeHashesApart(t *testing.T) {
	// Directory joins inventory and software on the same gate, so the
	// bookkeeping has to keep three hashes apart rather than two. A block
	// that did not go up must not be recorded as accepted: the server would
	// never be sent the membership it is missing.
	st := &enroll.State{Dir: t.TempDir()}
	st.Config.InventoryHash, st.Config.SoftwareHash = "inv", "soft"

	sent := sentFacts{gathered: true, directory: "dirhash"}
	if err := recordFacts(st, sent, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Config.DirectoryHash != "dirhash" {
		t.Errorf("DirectoryHash = %q, want dirhash", st.Config.DirectoryHash)
	}
	if st.Config.InventoryHash != "inv" || st.Config.SoftwareHash != "soft" {
		t.Errorf("an unsent block was recorded: inv=%q soft=%q",
			st.Config.InventoryHash, st.Config.SoftwareHash)
	}
}

func TestRecordFactsSavesWhenOnlyTheDirectoryMoved(t *testing.T) {
	// recordFacts skips the write when nothing it owns changed, comparing a
	// struct it builds by hand. A field left out of that struct is a hash
	// that is set in memory and lost on restart -- so the agent would
	// resend the same block after every service start, forever.
	dir := t.TempDir()
	st := &enroll.State{Dir: dir}
	if err := recordFacts(st, sentFacts{directory: "d1"}, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Config.DirectoryHash != "d1" {
		t.Errorf("DirectoryHash = %q after reload, want d1: the change was"+
			" never written to disk", reloaded.Config.DirectoryHash)
	}
}

func TestRecordFactsClearsWantWhenServerStopsAsking(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	st.Config.WantInventory, st.Config.WantSoftware = true, true
	st.Config.WantDirectory = true
	if err := recordFacts(st, sentFacts{gathered: true, inventory: "h"}, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The answer is authoritative. Or-ing the flags instead of assigning
	// would leave the agent gathering every poll forever.
	if st.Config.WantInventory || st.Config.WantSoftware || st.Config.WantDirectory {
		t.Error("want_* must follow the latest answer, not accumulate")
	}
}

func TestRecordFactsCarriesTheServersNewRequest(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	resp := &enroll.PollResponse{WantSoftware: true}
	if err := recordFacts(st, sentFacts{}, resp, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !st.Config.WantSoftware {
		t.Error("the server's request must survive to the next poll")
	}
	// Nothing was gathered, so nothing may be stamped: a failed or
	// skipped gather must not push the next one an hour out.
	if !st.Config.FactsChecked.IsZero() {
		t.Error("FactsChecked was stamped without a gather")
	}
}

func TestRecordFactsHonorsAndIgnoresTheGate(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name   string
		before bool
		gate   *bool
		want   bool
	}{
		// An old server sends no gate at all. Absent must not read as
		// "turned off", or every pre-facts server silently disables it.
		{"absent leaves it alone", false, nil, false},
		{"absent does not re-enable", true, nil, true},
		{"false disables", false, &off, true},
		{"true re-enables", true, &on, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &enroll.State{Dir: t.TempDir()}
			st.Config.FactsDisabled = tc.before
			resp := &enroll.PollResponse{CollectFacts: tc.gate}
			if err := recordFacts(st, sentFacts{}, resp, time.Now()); err != nil {
				t.Fatal(err)
			}
			if st.Config.FactsDisabled != tc.want {
				t.Errorf("FactsDisabled = %t, want %t", st.Config.FactsDisabled, tc.want)
			}
		})
	}
}

func TestRecordFactsPersistsThePrintersHash(t *testing.T) {
	// The same failure TestRecordFactsSavesWhenOnlyTheDirectoryMoved guards,
	// re-run for the fourth block. recordFacts skips the disk write when
	// nothing it owns changed, and it decides that by comparing a struct
	// listing the fields it owns -- so a new hash left out of that struct is
	// set in memory, never written, and resent after every service restart
	// for the life of the agent.
	dir := t.TempDir()
	st := &enroll.State{Dir: dir}
	if err := recordFacts(st, sentFacts{printers: "p1"}, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Config.PrintersHash != "p1" {
		t.Errorf("PrintersHash = %q after reload, want p1: the change was"+
			" never written to disk", reloaded.Config.PrintersHash)
	}
}

func TestRecordFactsKeepsThePrintersHashApartFromTheOthers(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	st.Config.InventoryHash, st.Config.SoftwareHash = "inv", "soft"
	st.Config.DirectoryHash = "dir"

	if err := recordFacts(st, sentFacts{gathered: true, printers: "p1"},
		&enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Config.PrintersHash != "p1" {
		t.Errorf("PrintersHash = %q", st.Config.PrintersHash)
	}
	if st.Config.InventoryHash != "inv" || st.Config.SoftwareHash != "soft" ||
		st.Config.DirectoryHash != "dir" {
		t.Errorf("an unsent block was recorded: inv=%q soft=%q dir=%q",
			st.Config.InventoryHash, st.Config.SoftwareHash, st.Config.DirectoryHash)
	}
}

func TestRecordFactsKeepsAnAcceptedPrintersHashWhenNothingWasSent(t *testing.T) {
	// The other direction, and the one a "record what was sent" loop gets
	// wrong by assigning unconditionally: a poll that carried no printers
	// block must leave the accepted hash alone. Clearing it would make the
	// agent resend the full printer list on the very next poll, every time
	// any other fact moved.
	st := &enroll.State{Dir: t.TempDir()}
	st.Config.PrintersHash = "accepted"

	if err := recordFacts(st, sentFacts{gathered: true, directory: "d1"},
		&enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Config.PrintersHash != "accepted" {
		t.Errorf("PrintersHash = %q, want accepted", st.Config.PrintersHash)
	}
}

func TestRecordFactsCarriesAndClearsTheServersPrinterRequest(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	if err := recordFacts(st, sentFacts{}, &enroll.PollResponse{WantPrinters: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !st.Config.WantPrinters {
		t.Fatal("the server asked for printers and the agent did not record it")
	}
	// The answer is authoritative: once the server holds the hash it stops
	// asking, and assigning rather than or-ing is what clears the flag.
	if err := recordFacts(st, sentFacts{}, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Config.WantPrinters {
		t.Error("the server stopped asking and the agent kept the flag set")
	}
}

func TestRecordFactsPersistsTheSecureBootHash(t *testing.T) {
	// The sixth block, and the same failure the printers and directory
	// versions of this test guard. recordFacts skips the disk write when
	// nothing it owns changed, and decides that from a struct listing the
	// fields it owns -- so a hash left out of that struct is set in memory,
	// never written, and resent after every service restart for the life
	// of the agent. The struct's own comment says as much.
	dir := t.TempDir()
	st := &enroll.State{Dir: dir}
	if err := recordFacts(st, sentFacts{secureboot: "sb1"}, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Config.SecureBootHash != "sb1" {
		t.Errorf("SecureBootHash = %q after reload, want sb1: the change was"+
			" never written to disk", reloaded.Config.SecureBootHash)
	}
}

func TestRecordFactsKeepsTheSecureBootHashApartFromTheOthers(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	st.Config.InventoryHash, st.Config.SoftwareHash = "inv", "soft"
	st.Config.DirectoryHash, st.Config.PrintersHash = "dir", "print"

	if err := recordFacts(st, sentFacts{gathered: true, secureboot: "sb1"},
		&enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Config.SecureBootHash != "sb1" {
		t.Errorf("SecureBootHash = %q", st.Config.SecureBootHash)
	}
	if st.Config.InventoryHash != "inv" || st.Config.SoftwareHash != "soft" ||
		st.Config.DirectoryHash != "dir" || st.Config.PrintersHash != "print" {
		t.Error("an unsent block was recorded")
	}
}

func TestRecordFactsCarriesAndClearsTheServersSecureBootRequest(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	if err := recordFacts(st, sentFacts{}, &enroll.PollResponse{WantSecureBoot: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !st.Config.WantSecureBoot {
		t.Fatal("the server asked for the block and the agent did not remember")
	}
	// The answer is authoritative: once the server holds a hash it stops
	// asking, and assigning rather than or-ing is what clears the flag.
	if err := recordFacts(st, sentFacts{secureboot: "sb1"}, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Config.WantSecureBoot {
		t.Error("the server stopped asking and the agent kept sending")
	}
}

func TestSecureBootRequestMakesTheFactsDue(t *testing.T) {
	// The per-kind hash comparison happens only after factsDue lets the
	// collectors run at all, so a want flag that is not in that gate can
	// wait an hour to be honoured.
	cfg := enroll.Config{FactsChecked: time.Now()}
	if factsDue(cfg, time.Now()) {
		t.Fatal("a just-checked agent is not due")
	}
	cfg.WantSecureBoot = true
	if !factsDue(cfg, time.Now()) {
		t.Error("the server asked for the Secure Boot block and the collectors did not run")
	}
}
