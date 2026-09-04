package identity

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"github.com/FOGProject/fog-agent/internal/identity/smbios"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemFirmwareTbl = kernel32.NewProc("GetSystemFirmwareTable")
)

// 'RSMB' as the FirmwareTableProviderSignature DWORD.
const providerRSMB = 0x52534D42

// rawSMBIOSData is the buffer layout GetSystemFirmwareTable('RSMB') returns.
type rawSMBIOSData struct {
	Used20CallingMethod byte
	MajorVersion        byte
	MinorVersion        byte
	DmiRevision         byte
	Length              uint32
	// SMBIOSTableData follows.
}

// readSMBIOS pulls the raw structure table from the firmware and parses it
// with the shared parser, so Windows and a raw-table Linux fallback agree
// byte for byte. No WMI, no COM, no CGO.
func readSMBIOS() Host {
	var h Host
	table, major, minor, err := RawSMBIOS()
	if err != nil {
		h.Warnings = append(h.Warnings, err.Error())
		return h
	}
	h.SMBIOSVersion = fmt.Sprintf("%d.%d", major, minor)
	id, perr := smbios.Parse(table, major, minor)
	if perr != nil {
		h.Warnings = append(h.Warnings, perr.Error())
	}
	h.Identity = id
	return h
}

// RawSMBIOS returns the firmware's structure table and its version.
//
// Exported because the inventory collector parses the same bytes for a
// different view (smbios.ParseHardware): reading the firmware twice, or
// reaching for WMI to get what is already in this buffer, would be two ways
// for the same machine to describe itself.
func RawSMBIOS() (table []byte, major, minor int, err error) {
	size, _, _ := procGetSystemFirmwareTbl.Call(providerRSMB, 0, 0, 0)
	if size == 0 {
		return nil, 0, 0, errors.New("GetSystemFirmwareTable: no RSMB table")
	}
	buf := make([]byte, size)
	n, _, callErr := procGetSystemFirmwareTbl.Call(providerRSMB, 0, uintptr(unsafe.Pointer(&buf[0])), size)
	if n == 0 {
		return nil, 0, 0, fmt.Errorf("GetSystemFirmwareTable: %v", callErr)
	}
	hdr := (*rawSMBIOSData)(unsafe.Pointer(&buf[0]))
	const hdrLen = 8
	table = buf[hdrLen:]
	if int(hdr.Length) < len(table) {
		table = table[:hdr.Length]
	}
	return table, int(hdr.MajorVersion), int(hdr.MinorVersion), nil
}
