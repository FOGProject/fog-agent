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

func TestRecordFactsClearsWantWhenServerStopsAsking(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	st.Config.WantInventory, st.Config.WantSoftware = true, true
	if err := recordFacts(st, sentFacts{gathered: true, inventory: "h"}, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The answer is authoritative. Or-ing the flags instead of assigning
	// would leave the agent gathering every poll forever.
	if st.Config.WantInventory || st.Config.WantSoftware {
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
