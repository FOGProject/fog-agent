//go:build windows

package netboot

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Win32 firmware calls want the namespace GUID as a string in braces,
// not as a parsed GUID.
var efiGlobalBraced = "{" + EFIGlobal + "}"

var (
	kernel32    = syscall.NewLazyDLL("kernel32.dll")
	procGetFirm = kernel32.NewProc("GetFirmwareEnvironmentVariableW")
	procSetFirm = kernel32.NewProc("SetFirmwareEnvironmentVariableW")
)

// ERROR_INVALID_FUNCTION is what the firmware calls return on a machine
// with no UEFI, which is the BIOS/CSM signal.
const errorInvalidFunction syscall.Errno = 1

// osReadVar reads a firmware variable. Unlike efivarfs there is no
// attribute word: the call returns the data bytes alone, measured on
// telliottwin11 2026-09-04 (design 0012 section 5).
func osReadVar(name string) ([]byte, error) {
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	g, err := syscall.UTF16PtrFromString(efiGlobalBraced)
	if err != nil {
		return nil, err
	}
	// Large enough for a BootOrder on a machine with a lot of entries and
	// for any single load option. A truncated read reports
	// ERROR_INSUFFICIENT_BUFFER rather than silently short-reading.
	buf := make([]byte, 8192)
	r, _, e := procGetFirm.Call(
		uintptr(unsafe.Pointer(n)),
		uintptr(unsafe.Pointer(g)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r == 0 {
		if en, ok := e.(syscall.Errno); ok && en == errorInvalidFunction {
			return nil, ErrUnsupported
		}
		return nil, fmt.Errorf("netboot: reading %s: %w", name, e)
	}
	return buf[:r], nil
}

func osWriteVar(name string, data []byte) error {
	if err := enableFirmwarePrivilege(); err != nil {
		return err
	}
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	g, err := syscall.UTF16PtrFromString(efiGlobalBraced)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("netboot: refusing to write %s with no data", name)
	}
	r, _, e := procSetFirm.Call(
		uintptr(unsafe.Pointer(n)),
		uintptr(unsafe.Pointer(g)),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
	)
	if r == 0 {
		if en, ok := e.(syscall.Errno); ok && en == errorInvalidFunction {
			return ErrUnsupported
		}
		return fmt.Errorf("netboot: writing %s: %w", name, e)
	}
	return nil
}

// osDeleteVar removes a variable. Setting it to zero bytes is how the Win32
// API expresses deletion; there is no separate delete call.
func osDeleteVar(name string) error {
	if err := enableFirmwarePrivilege(); err != nil {
		return err
	}
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	g, err := syscall.UTF16PtrFromString(efiGlobalBraced)
	if err != nil {
		return err
	}
	r, _, e := procSetFirm.Call(
		uintptr(unsafe.Pointer(n)),
		uintptr(unsafe.Pointer(g)),
		0,
		0,
	)
	if r == 0 {
		if en, ok := e.(syscall.Errno); ok && en == errorInvalidFunction {
			return ErrUnsupported
		}
		return fmt.Errorf("netboot: deleting %s: %w", name, e)
	}
	return nil
}

// enableFirmwarePrivilege turns on SE_SYSTEM_ENVIRONMENT_NAME in this
// process's token.
//
// Holding a privilege and having it enabled are different things: a token
// carries most privileges disabled, and the firmware write fails with
// ERROR_PRIVILEGE_NOT_HELD until AdjustTokenPrivileges switches this one
// on. The service runs as SYSTEM so the privilege is present to enable.
// Note the asymmetry with reading, which needs no privilege at all --
// measured in a UAC-filtered session for design 0012.
func enableFirmwarePrivilege() error {
	var tok windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY,
		&tok,
	); err != nil {
		return fmt.Errorf("netboot: opening the process token: %w", err)
	}
	defer tok.Close()

	var luid windows.LUID
	name, err := syscall.UTF16PtrFromString("SeSystemEnvironmentPrivilege")
	if err != nil {
		return err
	}
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return fmt.Errorf("netboot: looking up SeSystemEnvironmentPrivilege: %w", err)
	}
	priv := windows.Tokenprivileges{
		PrivilegeCount: 1,
	}
	priv.Privileges[0] = windows.LUIDAndAttributes{
		Luid:       luid,
		Attributes: windows.SE_PRIVILEGE_ENABLED,
	}
	if err := windows.AdjustTokenPrivileges(tok, false, &priv, 0, nil, nil); err != nil {
		return fmt.Errorf("netboot: enabling SeSystemEnvironmentPrivilege: %w", err)
	}
	// AdjustTokenPrivileges reports success even when it enabled nothing;
	// ERROR_NOT_ALL_ASSIGNED is delivered as the last error rather than as
	// a failed return, which is the one way this call lies.
	if err := windows.GetLastError(); err == windows.ERROR_NOT_ALL_ASSIGNED {
		return fmt.Errorf("netboot: this process does not hold SeSystemEnvironmentPrivilege")
	}
	return nil
}
