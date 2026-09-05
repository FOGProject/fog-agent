// Package firmware reads and writes UEFI variables.
//
// It exists because two things need them and the mechanics are not shared
// between platforms: internal/netboot arms BootNext (design 0013) and
// internal/secureboot reports the SecureBoot and SetupMode bytes (design
// 0012). Both would otherwise carry their own copy of the efivarfs
// attribute-word framing and the Win32 lazy-DLL calls, and two copies of
// this drift -- which is the failure both of those documents were written
// to avoid elsewhere.
//
// The layout difference is the thing worth knowing: efivarfs prepends a
// 4-byte attribute word to every value, on read and on write, and the Win32
// calls have no attribute word at all. Read returns the DATA in both cases,
// so callers never see the difference.
package firmware

import "errors"

// EFIGlobal is the EFI global variable namespace, where the boot manager
// variables and the Secure Boot state variables live.
const EFIGlobal = "8be4df61-93ca-11d2-aa0d-00e098032b8c"

// ErrUnsupported means there is no UEFI here to talk to: a BIOS/CSM
// machine, a kernel with no efivars mounted, or macOS. It is deliberately
// distinct from "that variable is not set", because the two mean different
// things to whoever has to fix it.
var ErrUnsupported = errors.New("firmware: UEFI variables are not available")

// Attributes for a variable that must survive a reboot and stay readable at
// runtime: non-volatile, boot services, runtime services.
const AttrsNVBSRT = 0x01 | 0x02 | 0x04

// ReadVar returns the data bytes of an EFI global variable, without any
// platform framing.
func ReadVar(name string) ([]byte, error) { return readVar(name) }

// WriteVar writes data to an EFI global variable with the NV|BS|RT
// attributes. Writing firmware variables needs privilege the reads do not:
// root on Linux, and SeSystemEnvironmentPrivilege enabled on Windows.
func WriteVar(name string, data []byte) error { return writeVar(name, data) }

// DeleteVar removes an EFI global variable.
func DeleteVar(name string) error { return deleteVar(name) }

// The OS-specific halves, swappable in tests.
var (
	readVar   = osReadVar
	writeVar  = osWriteVar
	deleteVar = osDeleteVar
)

// SetForTest replaces the platform accessors and returns a function that
// restores them. Only for tests in packages that build on this one.
func SetForTest(r func(string) ([]byte, error), w func(string, []byte) error, d func(string) error) func() {
	or, ow, od := readVar, writeVar, deleteVar
	if r != nil {
		readVar = r
	}
	if w != nil {
		writeVar = w
	}
	if d != nil {
		deleteVar = d
	}
	return func() { readVar, writeVar, deleteVar = or, ow, od }
}
