//go:build linux

package printers

import "testing"

// Real output, captured from a Fedora workstation with one network printer
// on 2026-09-04. Test doubles come from the emitting source, never from a
// belief about the format -- which is how the escaped spaces inside
// marker-names got into the tokenizer at all.
const (
	realLpstatV   = "device for Canon-MF650C-Series-UFR-II: socket://10.20.0.254\n"
	realLpstatD   = "no system default destination\n"
	realLpoptions = "copies=1 device-uri=socket://10.20.0.254 finishings=3 " +
		"job-cancel-after=10800 job-hold-until=no-hold job-priority=50 " +
		"job-sheets=none,none marker-change-time=1786186347 " +
		"marker-colors=#000000,#00FFFF,#FF00FF,#FFFF00 " +
		"marker-levels=-1,78,74,53 " +
		`marker-names='Canon\ Cartridge\ 067\ Black\ Toner,Canon\ Cartridge\ 067\ Cyan\ Toner' ` +
		"marker-types=toner printer-info=Canon-MF650C-Series-UFR-II " +
		"printer-is-accepting-jobs=true printer-is-shared=true " +
		"printer-location printer-make-and-model='Canon MF650C Series UFR II' " +
		"printer-state=3 printer-state-change-time=1786186347 " +
		"printer-type=8425692 printer-uri-supported=ipp://localhost/printers/Canon-MF650C-Series-UFR-II\n"
)

func TestParseLpstatVReadsNameAndURI(t *testing.T) {
	got := parseLpstatV(realLpstatV)
	if len(got) != 1 {
		t.Fatalf("got %d printers, want 1: %+v", len(got), got)
	}
	if got[0].Name != "Canon-MF650C-Series-UFR-II" {
		t.Errorf("name = %q", got[0].Name)
	}
	// The URI is the whole reason this reads `lpstat -v` and not the
	// legacy client's `lpstat -p`, which prints no URI at all.
	if got[0].URI != "socket://10.20.0.254" {
		t.Errorf("uri = %q", got[0].URI)
	}
}

func TestParseLpstatVIgnoresEverythingElse(t *testing.T) {
	// `lpstat -v` on a machine with no queues prints nothing; other lpstat
	// modes print lines that must not be mistaken for devices.
	for _, in := range []string{
		"",
		"lpstat: No destinations added.\n",
		"printer Canon is idle.  enabled since Thu 04 Sep 2026\n",
	} {
		if got := parseLpstatV(in); len(got) != 0 {
			t.Errorf("parseLpstatV(%q) = %+v, want none", in, got)
		}
	}
}

func TestParseLpstatDNoDefaultIsAnAnswerNotAFailure(t *testing.T) {
	if got := parseLpstatD(realLpstatD); got != "" {
		t.Errorf("got %q, want empty for a machine with no default", got)
	}
	const withDefault = "system default destination: Accounts-HP4550\n"
	if got := parseLpstatD(withDefault); got != "Accounts-HP4550" {
		t.Errorf("got %q", got)
	}
}

func TestParseLpoptionsKeepsSpacesInsideTheModelName(t *testing.T) {
	attrs := parseLpoptions(realLpoptions)
	// Splitting on whitespace would truncate this at "Canon", which is the
	// bug this tokenizer exists to avoid.
	if got := attrs["printer-make-and-model"]; got != "Canon MF650C Series UFR II" {
		t.Errorf("make-and-model = %q", got)
	}
	if got := attrs["printer-is-shared"]; got != "true" {
		t.Errorf("is-shared = %q", got)
	}
	// The backslash-escaped spaces inside marker-names must not have split
	// the token either, or every attribute after it would be misread.
	if got := attrs["printer-state"]; got != "3" {
		t.Errorf("printer-state = %q -- the tokenizer lost sync earlier in the line", got)
	}
}

func TestGatherUsesTheProbesAndFillsTheDriver(t *testing.T) {
	restore := func(v, d string, o func(string) (string, bool)) {
		runLpstatV = func() (string, bool) { return v, true }
		runLpstatD = func() (string, bool) { return d, true }
		runLpoptions = o
	}
	saveV, saveD, saveO := runLpstatV, runLpstatD, runLpoptions
	defer func() { runLpstatV, runLpstatD, runLpoptions = saveV, saveD, saveO }()

	restore(realLpstatV, "system default destination: Canon-MF650C-Series-UFR-II\n",
		func(string) (string, bool) { return realLpoptions, true })

	got, ok := Gather()
	if !ok {
		t.Fatal("Gather said no collector ran")
	}
	if got.Subsystem != SubsystemCUPS {
		t.Errorf("subsystem = %q", got.Subsystem)
	}
	if got.Default != "Canon-MF650C-Series-UFR-II" {
		t.Errorf("default = %q", got.Default)
	}
	if len(got.Installed) != 1 {
		t.Fatalf("got %d printers", len(got.Installed))
	}
	if got.Installed[0].Driver != "Canon MF650C Series UFR II" {
		t.Errorf("driver = %q", got.Installed[0].Driver)
	}
	if !got.Installed[0].Shared {
		t.Error("shared = false, but printer-is-shared=true")
	}
}

func TestNoCUPSReportsNothingRatherThanNoPrinters(t *testing.T) {
	saveV := runLpstatV
	defer func() { runLpstatV = saveV }()
	runLpstatV = func() (string, bool) { return "", false }

	got, ok := Gather()
	if ok {
		// The trap design 0006 section 6 names: the server treats a reported
		// list as complete, so an empty list from a machine that has no
		// spooler at all would wipe that host's printer history.
		t.Fatalf("Gather returned ok with %+v; a machine with no lpstat has "+
			"not answered the question, it has failed to be asked", got)
	}
}

func TestCUPSWithNoQueuesIsAnEmptyListNotSilence(t *testing.T) {
	saveV, saveD := runLpstatV, runLpstatD
	defer func() { runLpstatV, runLpstatD = saveV, saveD }()
	runLpstatV = func() (string, bool) { return "", true }
	runLpstatD = func() (string, bool) { return realLpstatD, true }

	got, ok := Gather()
	if !ok {
		t.Fatal("CUPS answered; the block must be sent")
	}
	if got.Installed == nil {
		t.Error("Installed is nil, which marshals to JSON null; the server " +
			"reads that differently from an empty list")
	}
	if len(got.Installed) != 0 {
		t.Errorf("got %+v", got.Installed)
	}
}
