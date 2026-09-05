package secureboot

import (
	"errors"
	"fmt"
	"testing"

	"github.com/FOGProject/fog-agent/internal/firmware"
)

// withVars swaps the firmware reader for a table of values, and for errors
// where a value is absent.
func withVars(t *testing.T, vals map[string][]byte, errs map[string]error) {
	t.Helper()
	old := readVar
	readVar = func(name string) ([]byte, error) {
		if e, ok := errs[name]; ok {
			return nil, e
		}
		if v, ok := vals[name]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("no variable %s", name)
	}
	t.Cleanup(func() { readVar = old })
}

// The lab machine that motivated design 0012: SecureBoot 01, SetupMode 00,
// which the server maps to "enforcing" while its ledger still said
// "disabled" from the last netboot.
func TestEnforcingMachineReportsTheBytesTheServerMaps(t *testing.T) {
	withVars(t, map[string][]byte{"SecureBoot": {0x01}, "SetupMode": {0x00}}, nil)
	got, ok := Gather()
	if !ok {
		t.Fatal("Gather declined to report on a UEFI machine")
	}
	want := State{Platform: "efi", SecureBoot: "01", SetupMode: "00"}
	if got != want {
		t.Errorf("Gather() = %+v, want %+v", got, want)
	}
}

// This is the byte-order trap from design 0012 section 5. A raw efivarfs
// read is 06 00 00 00 <value>; taking b[0] there yields 06, which is
// neither 00 nor 01, and every Linux host would land in NOEFIVARS.
// firmware.ReadVar strips the attribute word, so what arrives here is the
// value -- and this test fails the moment something stops stripping it.
func TestTheAttributeWordIsNotMistakenForTheValue(t *testing.T) {
	withVars(t, map[string][]byte{
		"SecureBoot": {0x01},
		"SetupMode":  {0x00},
	}, nil)
	got, _ := Gather()
	for _, v := range []string{got.SecureBoot, got.SetupMode} {
		if v == "06" {
			t.Fatalf("read the efivarfs attribute word as the value (%q)", v)
		}
		if v != "00" && v != "01" {
			t.Errorf("value %q is neither 00 nor 01", v)
		}
	}
}

func TestSetupModeMachine(t *testing.T) {
	withVars(t, map[string][]byte{"SecureBoot": {0x00}, "SetupMode": {0x01}}, nil)
	got, _ := Gather()
	if got.SetupMode != "01" {
		t.Errorf("SetupMode = %q, want 01", got.SetupMode)
	}
}

// A BIOS machine is a real answer, not a silence: it tells an admin the
// Secure Boot enrollment task can never apply, instead of leaving the host
// unreported forever.
func TestABIOSMachineReportsBIOS(t *testing.T) {
	withVars(t, nil, map[string]error{
		"SecureBoot": firmware.ErrUnsupported,
		"SetupMode":  firmware.ErrUnsupported,
	})
	got, ok := Gather()
	if !ok {
		t.Fatal("Gather declined to report a BIOS machine")
	}
	if got.Platform != "bios" {
		t.Errorf("Platform = %q, want bios", got.Platform)
	}
	if got.SecureBoot != "" || got.SetupMode != "" {
		t.Errorf("a BIOS machine reported values: %+v", got)
	}
}

// An unreadable variable must never render as "00". DISABLED is the value
// that makes a host look like a valid enrollment target, so "we could not
// read this" collapsing into it would stage certificates on machines the
// task can never run on. The server reads both-empty-on-efi as NOEFIVARS.
func TestAnUnreadableVariableIsEmptyAndNotZero(t *testing.T) {
	withVars(t, map[string][]byte{"SetupMode": {0x00}},
		map[string]error{"SecureBoot": errors.New("permission denied")})
	got, ok := Gather()
	if !ok {
		t.Fatal("Gather declined")
	}
	if got.Platform != "efi" {
		t.Errorf("Platform = %q, want efi -- one variable read fine, so there is UEFI here", got.Platform)
	}
	if got.SecureBoot != "" {
		t.Errorf("SecureBoot = %q, want the empty string", got.SecureBoot)
	}
	if got.SecureBoot == "00" {
		t.Error("an unreadable variable rendered as 00, which reads as DISABLED")
	}
}

func TestAnEmptyValueIsEmptyAndNotZero(t *testing.T) {
	withVars(t, map[string][]byte{"SecureBoot": {}, "SetupMode": {0x01}}, nil)
	got, _ := Gather()
	if got.SecureBoot != "" {
		t.Errorf("SecureBoot = %q, want the empty string for a zero-length value", got.SecureBoot)
	}
}

// macOS sends nothing. "nonefi" would assert that Secure Boot is not a
// concept on the machine, which is false for Apple platforms -- they have
// one, it is just not UEFI's. Sending nothing leaves the server's value
// alone.
func TestMacOSReportsNothing(t *testing.T) {
	old := goos
	goos = "darwin"
	t.Cleanup(func() { goos = old })
	withVars(t, map[string][]byte{"SecureBoot": {0x01}, "SetupMode": {0x00}}, nil)

	if _, ok := Gather(); ok {
		t.Error("macOS reported a Secure Boot state; it has no honest mapping onto the six names")
	}
}

func TestHashMovesWithEveryField(t *testing.T) {
	base := State{Platform: "efi", SecureBoot: "01", SetupMode: "00"}
	for _, other := range []State{
		{Platform: "bios", SecureBoot: "01", SetupMode: "00"},
		{Platform: "efi", SecureBoot: "00", SetupMode: "00"},
		{Platform: "efi", SecureBoot: "01", SetupMode: "01"},
		{Platform: "efi", SecureBoot: "", SetupMode: "00"},
	} {
		if base.Hash() == other.Hash() {
			t.Errorf("%+v and %+v hash the same, so a real change would never be sent", base, other)
		}
	}
	if base.Hash() != (State{Platform: "efi", SecureBoot: "01", SetupMode: "00"}).Hash() {
		t.Error("the same state hashed differently, so it would be resent every poll")
	}
}

// The separator matters: without it "efi"+"0"+"100" and "efi"+"01"+"00"
// would be one string and two different states would share a hash.
func TestFieldsCannotRunTogetherInTheHash(t *testing.T) {
	a := State{Platform: "efi", SecureBoot: "0", SetupMode: "100"}
	b := State{Platform: "efi", SecureBoot: "01", SetupMode: "00"}
	if a.Hash() == b.Hash() {
		t.Error("two different states share a hash; the fields run together")
	}
}
