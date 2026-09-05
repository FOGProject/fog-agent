// Package secureboot reports what the firmware says about Secure Boot
// (design 0012).
//
// It reports OBSERVATIONS, not a verdict: the platform, and the raw
// SecureBoot and SetupMode bytes. The server maps those three onto its six
// state names with FOG\Boot\SecureBootState::fromBootRequest(), the same
// call that maps the ones iPXE sends. Computing the name here would put
// that six-way mapping in two codebases in two languages, and the
// vocabulary was copied verbatim from FOS's sbState() precisely so there
// would only ever be one of it.
package secureboot

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/FOGProject/fog-agent/internal/firmware"
)

// State is the block the agent puts in the poll request. The field names
// and the "00"/"01" spelling are the shape fromBootRequest() already
// accepts from a boot request.
type State struct {
	// Platform is "efi" or "bios". Anything but "efi" makes the server
	// answer NONEFI, so it must not be guessed.
	Platform string `json:"platform"`
	// SecureBoot and SetupMode are the variables' single data bytes, hex,
	// or "" when the variable could not be read. Both empty on an EFI
	// machine is what the server reads as NOEFIVARS.
	SecureBoot string `json:"secure_boot"`
	SetupMode  string `json:"setup_mode"`
}

// Hash is the change marker for the fact channel: the same block twice does
// not go on the wire twice.
func (s State) Hash() string {
	return fmt.Sprintf("%x", []byte(s.Platform+"|"+s.SecureBoot+"|"+s.SetupMode))
}

// readVar is swapped in tests.
var readVar = firmware.ReadVar

// goos is swapped in tests, so the macOS decision is testable from Linux.
var goos = runtime.GOOS

// Gather reads the two variables. The bool is false when the agent should
// send nothing at all, which is not the same as sending "no Secure Boot
// here".
//
// macOS is the false case. Apple's platforms have a secure boot model that
// is not UEFI Secure Boot, and there is no honest mapping onto the six
// names; reporting "nonefi" would assert "Secure Boot is not a concept on
// this machine", which is false. Sending nothing leaves the server's
// existing value alone, which is the truthful outcome.
func Gather() (State, bool) {
	if goos == "darwin" {
		return State{}, false
	}
	sb, sbErr := readVar("SecureBoot")
	sm, smErr := readVar("SetupMode")

	// No UEFI at all. Reporting this is worth doing: "nonefi" is a real
	// answer that tells an admin the Secure Boot enrollment task can
	// never apply here, rather than leaving the host unreported forever.
	if errors.Is(sbErr, firmware.ErrUnsupported) && errors.Is(smErr, firmware.ErrUnsupported) {
		return State{Platform: "bios"}, true
	}
	return State{
		Platform:   "efi",
		SecureBoot: firstByteHex(sb, sbErr),
		SetupMode:  firstByteHex(sm, smErr),
	}, true
}

// firstByteHex renders a variable's value the way the server expects it.
//
// firmware.ReadVar has already stripped efivarfs's 4-byte attribute word,
// so what arrives here is the data on both platforms and the byte wanted is
// the first one. Reading the raw file instead would need the LAST byte on
// Linux and the first on Windows -- efivarfs prepends the attributes and
// the Win32 call does not -- and taking b[0] off a raw Linux read yields
// 0x06, which is neither "00" nor "01" and would put every Linux host in
// NOEFIVARS.
//
// An unreadable variable is "", never "00": "we could not read this" must
// not collapse into the one answer that makes a host look like a valid
// enrollment target.
func firstByteHex(b []byte, err error) string {
	if err != nil || len(b) == 0 {
		return ""
	}
	return fmt.Sprintf("%02x", b[0])
}
