package printerset

import (
	"context"
	"strings"
	"testing"

	facts "github.com/FOGProject/fog-agent/internal/printers"
)

func installed(ps ...facts.Printer) map[string]facts.Printer {
	m := make(map[string]facts.Printer, len(ps))
	for _, p := range ps {
		m[p.Name] = p
	}
	return m
}

func TestDecideInstallsWhatIsMissing(t *testing.T) {
	got, why := Decide(
		Printer{Name: "Accounts", URI: "socket://h:9100"},
		installed(),
	)
	if got != Add {
		t.Fatalf("got %s (%s), want add", got, why)
	}
}

func TestDecideUpdatesWhenTheURIMoved(t *testing.T) {
	got, why := Decide(
		Printer{Name: "Accounts", URI: "socket://new:9100"},
		installed(facts.Printer{Name: "Accounts", URI: "socket://old:9100"}),
	)
	if got != Update {
		t.Fatalf("got %s, want update", got)
	}
	// The reason has to name both, or an admin reading the log cannot tell
	// which way round the change went.
	if !strings.Contains(why, "socket://old:9100") ||
		!strings.Contains(why, "socket://new:9100") {
		t.Errorf("why = %q", why)
	}
}

func TestDecideDoesNothingWhenTheURIMatches(t *testing.T) {
	got, _ := Decide(
		Printer{Name: "Accounts", URI: "socket://h:9100"},
		installed(facts.Printer{Name: "Accounts", URI: "socket://h:9100"}),
	)
	if got != None {
		t.Fatalf("got %s, want none", got)
	}
}

func TestDecideIgnoresTheDriverEntirely(t *testing.T) {
	// The load-bearing one. The observed driver comes from the spooler's own
	// vocabulary -- printer-make-and-model on CUPS reads "Canon MF650C
	// Series UFR II" -- and it is not the string passed to `lpadmin -m`,
	// which is a model URI or a PPD path. Comparing them finds a difference
	// on every poll, orders an update that changes nothing, and finds the
	// same difference again forever: lpadmin against every printer in the
	// estate every five minutes.
	got, _ := Decide(
		Printer{Name: "Accounts", URI: "socket://h:9100", Driver: "everywhere"},
		installed(facts.Printer{
			Name: "Accounts", URI: "socket://h:9100",
			Driver: "Canon MF650C Series UFR II",
		}),
	)
	if got != None {
		t.Fatalf("got %s -- comparing the driver is a hot loop, not a fix", got)
	}
}

func TestDecideRefusesToActWithoutAURI(t *testing.T) {
	// Not this machine's failure: the server could neither derive a URI nor
	// be given one. Installing a queue pointed at nothing would be worse
	// than saying so.
	got, why := Decide(Printer{Name: "Accounts"}, installed())
	if got != None {
		t.Fatalf("got %s, want none", got)
	}
	if !strings.Contains(why, "no device URI") {
		t.Errorf("why = %q -- the message is what reaches the admin", why)
	}
}

func TestSameURIFoldsTheSchemeAndNothingElse(t *testing.T) {
	if !sameURI("SOCKET://h:9100", "socket://h:9100") {
		t.Error("the scheme is case-insensitive")
	}
	// A queue path is case-sensitive on the far end, so these are not the
	// same share on a case-sensitive server.
	if sameURI("smb://srv/HP4550", "smb://srv/hp4550") {
		t.Error("the path must not be folded")
	}
}

func TestUnwantedOnlyInExclusiveMode(t *testing.T) {
	obs := []facts.Printer{{Name: "Mine"}, {Name: "Assigned"}}
	policy := Policy{Printers: []Printer{{Name: "Assigned"}}}

	for _, mode := range []string{ManageOff, ManageAssigned, ""} {
		policy.Manage = mode
		if got := Unwanted(policy, obs); len(got) != 0 {
			t.Errorf("mode %q removed %v; only exclusive removes anything", mode, got)
		}
	}

	policy.Manage = ManageExclusive
	got := Unwanted(policy, obs)
	if len(got) != 1 || got[0] != "Mine" {
		t.Fatalf("got %v, want [Mine]", got)
	}
}

func TestUnwantedNeverReturnsAnUnnamedQueue(t *testing.T) {
	// There would be nothing to pass to the backend, and remove-everything
	// is exactly the shape this must not have.
	got := Unwanted(
		Policy{Manage: ManageExclusive},
		[]facts.Printer{{Name: ""}, {Name: "  "}},
	)
	for _, n := range got {
		if strings.TrimSpace(n) == "" {
			t.Fatalf("returned an unnamed queue: %q", n)
		}
	}
}

// fakeBackend records what it was asked to do.
type fakeBackend struct {
	added      []Printer
	removed    []string
	defaulted  []string
	failAdd    bool
	addMessage string
}

func (f *fakeBackend) Available() (bool, string) { return true, "" }
func (f *fakeBackend) Add(_ context.Context, p Printer) Result {
	f.added = append(f.added, p)
	if f.failAdd {
		return Result{Status: StatusFailed, Error: f.addMessage}
	}
	return Result{Status: StatusInstalled}
}
func (f *fakeBackend) Remove(_ context.Context, n string) Result {
	f.removed = append(f.removed, n)
	return Result{Status: StatusRemoved}
}
func (f *fakeBackend) SetDefault(_ context.Context, n string) Result {
	f.defaulted = append(f.defaulted, n)
	return Result{Status: StatusUpdated}
}

func TestConvergeDoesNothingWhenManagementIsOff(t *testing.T) {
	b := &fakeBackend{}
	for _, mode := range []string{ManageOff, ""} {
		reports := Converge(context.Background(), b,
			Policy{Manage: mode, Printers: []Printer{{Name: "A", URI: "socket://h:9100"}}},
			facts.Printers{})
		if len(reports) != 0 || len(b.added) != 0 {
			t.Fatalf("mode %q acted: %d report(s), %d add(s)", mode, len(reports), len(b.added))
		}
	}
}

func TestConvergeReportsEveryAssignedPrinterIncludingTheQuietOnes(t *testing.T) {
	// The server reads a converged report as "still true" and a silent
	// printer as unknown, so a printer that needed nothing still reports.
	b := &fakeBackend{}
	policy := Policy{
		Manage: ManageAssigned,
		Printers: []Printer{
			{Name: "Fine", URI: "socket://h:9100"},
			{Name: "Missing", URI: "ipp://p/ipp/print"},
		},
	}
	obs := facts.Printers{Installed: []facts.Printer{
		{Name: "Fine", URI: "socket://h:9100"},
	}}

	reports := Converge(context.Background(), b, policy, obs)
	if len(reports) != 2 {
		t.Fatalf("got %d report(s), want one per assigned printer", len(reports))
	}
	byName := map[string]Report{}
	for _, r := range reports {
		byName[r.Printer.Name] = r
	}
	if byName["Fine"].Status != StatusConverged {
		t.Errorf("Fine = %q", byName["Fine"].Status)
	}
	if byName["Missing"].Status != StatusInstalled {
		t.Errorf("Missing = %q", byName["Missing"].Status)
	}
	if len(b.added) != 1 || b.added[0].Name != "Missing" {
		t.Errorf("added %v; only the missing one should have been touched", b.added)
	}
}

func TestConvergeCallsAnUpdateAnUpdate(t *testing.T) {
	b := &fakeBackend{}
	reports := Converge(context.Background(), b,
		Policy{Manage: ManageAssigned, Printers: []Printer{
			{Name: "A", URI: "socket://new:9100"}}},
		facts.Printers{Installed: []facts.Printer{
			{Name: "A", URI: "socket://old:9100"}}})
	if len(reports) != 1 || reports[0].Status != StatusUpdated {
		t.Fatalf("got %+v, want one updated", reports)
	}
}

func TestConvergeReportsAFailureWithItsMessage(t *testing.T) {
	b := &fakeBackend{failAdd: true, addMessage: "lpadmin: Bad device-URI scheme"}
	reports := Converge(context.Background(), b,
		Policy{Manage: ManageAssigned, Printers: []Printer{
			{Name: "A", URI: "gopher://h/"}}},
		facts.Printers{})
	if len(reports) != 1 {
		t.Fatalf("got %d reports", len(reports))
	}
	if reports[0].Status != StatusFailed {
		t.Errorf("status = %q", reports[0].Status)
	}
	// The message is the whole point: today a printer that will not install
	// produces nothing an admin can see.
	if reports[0].Error != "lpadmin: Bad device-URI scheme" {
		t.Errorf("error = %q", reports[0].Error)
	}
}

func TestConvergeTruncatesAProvidersNovel(t *testing.T) {
	b := &fakeBackend{failAdd: true, addMessage: strings.Repeat("x", 4000)}
	reports := Converge(context.Background(), b,
		Policy{Manage: ManageAssigned,
			Printers: []Printer{{Name: "A", URI: "socket://h:9100"}}},
		facts.Printers{})
	if len(reports[0].Error) != MaxError {
		t.Fatalf("kept %d bytes; the server column holds %d", len(reports[0].Error), MaxError)
	}
}

func TestConvergeRemovesOnlyInExclusiveMode(t *testing.T) {
	obs := facts.Printers{Installed: []facts.Printer{
		{Name: "Assigned", URI: "socket://h:9100"},
		{Name: "Someone's own", URI: "usb://x"},
	}}
	policy := Policy{Manage: ManageAssigned, Printers: []Printer{
		{Name: "Assigned", URI: "socket://h:9100"}}}

	b := &fakeBackend{}
	Converge(context.Background(), b, policy, obs)
	if len(b.removed) != 0 {
		t.Fatalf("assigned mode removed %v; it must leave unmanaged printers alone", b.removed)
	}

	policy.Manage = ManageExclusive
	b = &fakeBackend{}
	Converge(context.Background(), b, policy, obs)
	if len(b.removed) != 1 || b.removed[0] != "Someone's own" {
		t.Fatalf("got %v, want [Someone's own]", b.removed)
	}
}

func TestConvergeSetsTheDefaultOnlyWhenItIsWrong(t *testing.T) {
	policy := Policy{Manage: ManageAssigned, Default: "A",
		Printers: []Printer{{Name: "A", URI: "socket://h:9100"}}}
	obs := facts.Printers{Default: "A", Installed: []facts.Printer{
		{Name: "A", URI: "socket://h:9100"}}}

	b := &fakeBackend{}
	Converge(context.Background(), b, policy, obs)
	if len(b.defaulted) != 0 {
		t.Fatalf("set the default when it was already right: %v", b.defaulted)
	}

	obs.Default = "B"
	b = &fakeBackend{}
	Converge(context.Background(), b, policy, obs)
	if len(b.defaulted) != 1 || b.defaulted[0] != "A" {
		t.Fatalf("got %v, want [A]", b.defaulted)
	}
}

func TestConvergeReportsAMissingURIAsAFailure(t *testing.T) {
	b := &fakeBackend{}
	reports := Converge(context.Background(), b,
		Policy{Manage: ManageAssigned, Printers: []Printer{{Name: "A"}}},
		facts.Printers{})
	if len(reports) != 1 || reports[0].Status != StatusFailed {
		t.Fatalf("got %+v, want one failed", reports)
	}
	if !strings.Contains(reports[0].Error, "no device URI") {
		t.Errorf("error = %q -- this is the line the admin reads", reports[0].Error)
	}
	if len(b.added) != 0 {
		t.Error("attempted an install against a printer with no address")
	}
}

func TestDriverArgsSpellsDriverlessCorrectly(t *testing.T) {
	// FOG's model has pDefFile and pModel and no way to say "neither", so
	// an empty driver over IPP means driverless: -m everywhere.
	got := driverArgs("", "ipp://p.corp/ipp/print")
	if len(got) != 2 || got[0] != "-m" || got[1] != "everywhere" {
		t.Errorf("empty driver over ipp -> %v", got)
	}
	if got := driverArgs("  ", "ipps://p.corp/ipp/print"); len(got) != 2 || got[1] != "everywhere" {
		t.Errorf("whitespace driver over ipps -> %v", got)
	}

	// The one the lab caught on 2026-09-04. An empty driver at a socket://
	// device is a RAW queue, and lpadmin makes one when given no -m at
	// all. Asking for everywhere there is REFUSED -- "IPP Everywhere
	// driver requires an IPP connection" -- so the queue is never created,
	// which is the common case for a FOG printer defined the oldest way.
	for _, uri := range []string{
		"socket://10.0.4.20:9100", "lpd://10.0.4.20/queue", "smb://srv/HP4550", "",
	} {
		if got := driverArgs("", uri); len(got) != 0 {
			t.Errorf("empty driver at %q -> %v, want a raw queue (no args)", uri, got)
		}
	}

	if got := driverArgs("/usr/share/ppd/x.PPD", "socket://h:9100"); got[0] != "-P" {
		t.Errorf("a PPD path goes in with -P, got %v", got)
	}
	if got := driverArgs("HP Universal Printing PCL 6", "socket://h:9100"); got[0] != "-m" {
		t.Errorf("a model name goes in with -m, got %v", got)
	}
}

func TestPSQuoteMakesAPrinterNameDataNotScript(t *testing.T) {
	// PowerShell does no expansion inside single quotes, so $ and ` are
	// literal; doubling an embedded quote is the whole escaping rule.
	if got := psQuote("It's $env:PATH"); got != `'It''s $env:PATH'` {
		t.Fatalf("got %s", got)
	}
	if strings.Count(psQuote("a'b"), "'") != 4 {
		t.Error("an embedded quote must be doubled, or it closes the literal")
	}
}

func TestPortForReversesTheURIReconstruction(t *testing.T) {
	// The mirror of what internal/printers does when reporting, and the
	// reason one printer row serves both platforms.
	cases := map[string]string{
		"socket://10.0.4.20:9100": "IP_10.0.4.20_9100",
		"socket://10.0.4.20":      "IP_10.0.4.20_9100",
		"lpd://10.0.4.20/queue":   "LPR_10.0.4.20_queue",
		"ipp://p.corp/ipp/print":  "ipp://p.corp/ipp/print",
		"smb://srv/HP4550":        `\\srv\HP4550`,
	}
	for uri, want := range cases {
		got, err := portFor(uri)
		if err != nil {
			t.Errorf("portFor(%q): %v", uri, err)
			continue
		}
		if got.name != want {
			t.Errorf("portFor(%q).name = %q, want %q", uri, got.name, want)
		}
		if got.create == "" {
			t.Errorf("portFor(%q) produced no creation script", uri)
		}
	}
}

func TestPortForRefusesWhatItCannotBuild(t *testing.T) {
	for _, uri := range []string{
		// iPrint is driven by iprntcmd.exe, a Micro Focus client tool this
		// agent does not ship and must not assume is present.
		"iprint://server",
		"usb://Canon/MF650C",
		"port:USB001",
	} {
		_, err := portFor(uri)
		if err == nil {
			t.Errorf("portFor(%q) succeeded; it must say it cannot rather than guess", uri)
			continue
		}
		if _, ok := err.(errUnsupportedScheme); !ok {
			t.Errorf("portFor(%q) -> %T, want errUnsupportedScheme", uri, err)
		}
	}

	// Nothing that is a scheme, so nothing to name in the message. Saying
	// ":// printers are not supported on this platform" about an empty
	// scheme is the wrong sentence in the admin's report.
	for _, uri := range []string{"", "   ", "not a uri", "Accounts"} {
		_, err := portFor(uri)
		if err == nil {
			t.Errorf("portFor(%q) succeeded", uri)
			continue
		}
		if _, ok := err.(errBadURI); !ok {
			t.Errorf("portFor(%q) -> %v (%T), want errBadURI", uri, err, err)
		}
	}
}
