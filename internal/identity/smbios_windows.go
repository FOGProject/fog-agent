package identity

import (
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
	size, _, _ := procGetSystemFirmwareTbl.Call(providerRSMB, 0, 0, 0)
	if size == 0 {
		h.Warnings = append(h.Warnings, "GetSystemFirmwareTable: no RSMB table")
		return h
	}
	buf := make([]byte, size)
	n, _, err := procGetSystemFirmwareTbl.Call(providerRSMB, 0, uintptr(unsafe.Pointer(&buf[0])), size)
	if n == 0 {
		h.Warnings = append(h.Warnings, fmt.Sprintf("GetSystemFirmwareTable: %v", err))
		return h
	}
	hdr := (*rawSMBIOSData)(unsafe.Pointer(&buf[0]))
	const hdrLen = 8
	table := buf[hdrLen:]
	if int(hdr.Length) < len(table) {
		table = table[:hdr.Length]
	}
	h.SMBIOSVersion = fmt.Sprintf("%d.%d", hdr.MajorVersion, hdr.MinorVersion)
	id, perr := smbios.Parse(table, int(hdr.MajorVersion), int(hdr.MinorVersion))
	if perr != nil {
		h.Warnings = append(h.Warnings, perr.Error())
	}
	h.Identity = id
	return h
}
