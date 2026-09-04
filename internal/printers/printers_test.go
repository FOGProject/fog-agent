package printers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeSortsSoAnUnchangedMachineHashesTheSame(t *testing.T) {
	// Neither EnumPrinters nor lpstat promises an order. Without the sort
	// the hash would move on a machine where nothing changed, the block
	// would be resent every poll, and the gate would buy nothing.
	a := Printers{Subsystem: "cups", Installed: []Printer{
		{Name: "Reception", URI: "ipp://p/ipp/print"},
		{Name: "Accounts", URI: "socket://10.0.4.20:9100"},
	}}
	b := Printers{Subsystem: "cups", Installed: []Printer{
		{Name: "Accounts", URI: "socket://10.0.4.20:9100"},
		{Name: "Reception", URI: "ipp://p/ipp/print"},
	}}
	if a.normalize().Hash() != b.normalize().Hash() {
		t.Fatal("the same two printers in a different order hash differently")
	}
}

func TestNormalizeDropsUnnamedAndDuplicateQueues(t *testing.T) {
	got := Printers{Installed: []Printer{
		{Name: "  "},
		{Name: "Accounts", Driver: "first"},
		{Name: "Accounts", Driver: "second"},
	}}.normalize()
	if len(got.Installed) != 1 {
		t.Fatalf("got %+v", got.Installed)
	}
	if got.Installed[0].Driver != "first" {
		t.Errorf("kept the wrong duplicate: %q", got.Installed[0].Driver)
	}
}

func TestNormalizeClearsADefaultThatIsNotInstalled(t *testing.T) {
	// Left behind by a removal that did not clear the setting. Reporting it
	// would put the host into a drift no action can resolve.
	got := Printers{
		Default:   "Gone",
		Installed: []Printer{{Name: "Accounts"}},
	}.normalize()
	if got.Default != "" {
		t.Errorf("default = %q, want empty", got.Default)
	}
}

func TestNormalizeKeepsADefaultThatIsInstalled(t *testing.T) {
	got := Printers{
		Default:   "Accounts",
		Installed: []Printer{{Name: "Accounts"}},
	}.normalize()
	if got.Default != "Accounts" {
		t.Errorf("default = %q", got.Default)
	}
}

func TestNormalizeURILowercasesOnlyTheScheme(t *testing.T) {
	cases := map[string]string{
		"SOCKET://10.0.4.20:9100": "socket://10.0.4.20:9100",
		// The queue path is case-sensitive on the far end, so folding it
		// would turn an exact comparison into one that sometimes lies.
		"SMB://srv/HP4550": "smb://srv/HP4550",
		"":                 "",
		"USB001":           "USB001",
	}
	for in, want := range cases {
		if got := normalizeURI(in); got != want {
			t.Errorf("normalizeURI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpaqueURICannotBeMistakenForAnAddress(t *testing.T) {
	got := opaqueURI("USB001")
	if got != "port:USB001" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "//") {
		t.Error("an unaddressable port must not render as an authority-style URI")
	}
	if opaqueURI("  ") != "" {
		t.Error("an empty port name is not a port")
	}
}

func TestEmptyInstalledMarshalsAsAListNotNull(t *testing.T) {
	b, err := json.Marshal(Printers{Subsystem: "cups"}.normalize())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"installed":[]`) {
		t.Fatalf("got %s -- null and [] are different statements to the server", b)
	}
}

func TestHashMovesWhenAnyReportedFieldMoves(t *testing.T) {
	base := Printers{Subsystem: "cups", Default: "A", Installed: []Printer{
		{Name: "A", URI: "socket://h:9100", Driver: "d", Shared: false},
	}}.normalize()

	mutations := map[string]Printers{
		"subsystem": {Subsystem: "winspool", Default: "A", Installed: base.Installed},
		"default":   {Subsystem: "cups", Installed: base.Installed},
		"uri": {Subsystem: "cups", Default: "A", Installed: []Printer{
			{Name: "A", URI: "socket://h:9101", Driver: "d"}}},
		"driver": {Subsystem: "cups", Default: "A", Installed: []Printer{
			{Name: "A", URI: "socket://h:9100", Driver: "other"}}},
		"shared": {Subsystem: "cups", Default: "A", Installed: []Printer{
			{Name: "A", URI: "socket://h:9100", Driver: "d", Shared: true}}},
		"added": {Subsystem: "cups", Default: "A", Installed: []Printer{
			{Name: "A", URI: "socket://h:9100", Driver: "d"}, {Name: "B"}}},
	}
	for what, m := range mutations {
		if m.normalize().Hash() == base.Hash() {
			t.Errorf("changing the %s did not move the hash, so that change "+
				"would never be reported", what)
		}
	}
}
