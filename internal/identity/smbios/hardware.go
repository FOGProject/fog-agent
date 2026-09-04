package smbios

import (
	"encoding/binary"
	"strconv"
)

// Hardware is the inventory view of the same structure table Parse reads
// for identity. Two views rather than one struct because they answer
// different questions and have different consumers: identity is the tuple
// enrollment binds a certificate to and must never drift, inventory is a
// page of descriptive strings for an admin to read. Adding a field here
// cannot change what a machine enrolls as.
//
// Values are raw, as Parse's are: canonicalization is the server's job so
// PXE, FOS and the agent obey one rule set.
type Hardware struct {
	SysMan     string
	SysProduct string
	SysVersion string
	SysSerial  string

	BIOSVendor  string
	BIOSVersion string
	BIOSDate    string

	MBMan         string
	MBProductName string
	MBVersion     string
	MBSerial      string
	MBAsset       string

	CaseMan string
	CaseVer string
	// CaseType is the raw SMBIOS chassis type as a decimal string, the same
	// shape Linux's /sys/class/dmi/id/chassis_type gives, so one mapping to
	// a display name serves both platforms.
	CaseType   string
	CaseSerial string
	CaseAsset  string

	CPUMan     string
	CPUVersion string
	// CPUCurrent and CPUMax are MHz as decimal strings, empty when the
	// firmware reports zero (which means "unknown", not "0 MHz").
	CPUCurrent string
	CPUMax     string

	// Mem is total installed memory in whole MB, summed across populated
	// memory devices.
	Mem string
}

// Structure type codes this view reads, in addition to the identity ones.
const (
	typeBIOS      = 0
	typeProcessor = 4
	typeMemory    = 17
)

// ParseHardware walks a raw SMBIOS structure table and returns the
// descriptive hardware fields. major/minor is the version from the entry
// point, unused here today but taken so the signature matches Parse and a
// version-gated field can be added without changing every caller.
//
// A malformed table is an error rather than a partial result: the caller
// reports no inventory block at all in that case, and a half-read row is
// worse than none -- the server would take it as the whole truth.
func ParseHardware(table []byte, major, minor int) (Hardware, error) {
	var hw Hardware
	var memKB uint64
	off := 0
	for off+4 <= len(table) {
		length := int(table[off+1])
		if length < 4 || off+length > len(table) {
			return hw, structureError(length, off)
		}
		formatted := table[off : off+length]
		strs, next, err := stringSet(table, off+length)
		if err != nil {
			return hw, err
		}
		switch table[off] {
		case typeBIOS:
			hw.BIOSVendor = str(strs, formatted, 0x04)
			hw.BIOSVersion = str(strs, formatted, 0x05)
			hw.BIOSDate = str(strs, formatted, 0x08)
		case typeSystem:
			hw.SysMan = str(strs, formatted, 0x04)
			hw.SysProduct = str(strs, formatted, 0x05)
			hw.SysVersion = str(strs, formatted, 0x06)
			hw.SysSerial = str(strs, formatted, 0x07)
		case typeBaseboard:
			hw.MBMan = str(strs, formatted, 0x04)
			hw.MBProductName = str(strs, formatted, 0x05)
			hw.MBVersion = str(strs, formatted, 0x06)
			hw.MBSerial = str(strs, formatted, 0x07)
			hw.MBAsset = str(strs, formatted, 0x08)
		case typeChassis:
			hw.CaseMan = str(strs, formatted, 0x04)
			if length > 0x05 {
				// Bit 7 is the chassis-lock flag, not part of the type.
				hw.CaseType = strconv.Itoa(int(formatted[0x05] & 0x7F))
			}
			hw.CaseVer = str(strs, formatted, 0x06)
			hw.CaseSerial = str(strs, formatted, 0x07)
			hw.CaseAsset = str(strs, formatted, 0x08)
		case typeProcessor:
			// The first populated central processor. A second socket
			// repeats the same model, and the inventory row holds one.
			if hw.CPUVersion == "" && populatedCPU(formatted, length) {
				hw.CPUMan = str(strs, formatted, 0x07)
				hw.CPUVersion = str(strs, formatted, 0x10)
				hw.CPUMax = mhz(formatted, length, 0x14)
				hw.CPUCurrent = mhz(formatted, length, 0x16)
			}
		case typeMemory:
			memKB += memoryDeviceKB(formatted, length)
		case typeEnd:
			hw.Mem = totalMemMB(memKB)
			return hw, nil
		}
		off = next
	}
	hw.Mem = totalMemMB(memKB)
	return hw, nil
}

// populatedCPU reports whether a type 4 structure describes a central
// processor in an occupied socket. An empty socket still gets a structure,
// with an empty version string, and reporting it would blank the field on
// a machine whose real CPU is described by the next one.
func populatedCPU(formatted []byte, length int) bool {
	if length <= 0x18 {
		// Too short to carry Status; fall back to trusting the entry, since
		// a pre-2.0 table with a bogus socket is not a case worth losing a
		// real CPU over.
		return true
	}
	// Processor Type 3 is "Central Processor"; Status bit 6 is "socket
	// populated".
	return formatted[0x05] == 3 && formatted[0x18]&0x40 != 0
}

// mhz reads a WORD speed field. Zero means unknown per the spec, and an
// empty string is how the rest of the agent says "not reported" -- storing
// "0" would render as a 0 MHz processor on the Inventory tab.
func mhz(formatted []byte, length, at int) string {
	if length < at+2 {
		return ""
	}
	v := binary.LittleEndian.Uint16(formatted[at : at+2])
	if v == 0 {
		return ""
	}
	return strconv.Itoa(int(v))
}

// memoryDeviceKB returns one memory device's size in KB.
//
// The Size field is a WORD whose bit 15 picks the unit -- set means KB,
// clear means MB -- which is why this returns KB and the caller divides
// once at the end rather than rounding a 512 KB module to zero. 0 is an
// unpopulated slot and 0xFFFF is unknown; 0x7FFF means the real size is in
// the Extended Size DWORD, added in SMBIOS 2.7 for modules over 32 GB.
func memoryDeviceKB(formatted []byte, length int) uint64 {
	if length < 0x0E {
		return 0
	}
	size := binary.LittleEndian.Uint16(formatted[0x0C:0x0E])
	switch size {
	case 0, 0xFFFF:
		return 0
	case 0x7FFF:
		if length < 0x20 {
			return 0
		}
		// Extended Size is in MB, and its top bit is reserved.
		ext := binary.LittleEndian.Uint32(formatted[0x1C:0x20]) & 0x7FFFFFFF
		return uint64(ext) * 1024
	}
	if size&0x8000 != 0 {
		return uint64(size & 0x7FFF)
	}
	return uint64(size) * 1024
}

// totalMemMB renders the summed size as whole MB, empty when nothing was
// reported: an inventory row saying 0 MB of memory is worse than one
// saying nothing.
func totalMemMB(kb uint64) string {
	if kb == 0 {
		return ""
	}
	return strconv.FormatUint(kb/1024, 10)
}
