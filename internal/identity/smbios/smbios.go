// Package smbios parses the raw SMBIOS structure table into the four
// identity fields fogproject records at PXE boot (see fogproject PR #1668,
// FOG\Base\SmbiosIdentity): system UUID, system serial, baseboard serial,
// chassis asset tag.
//
// Values are returned raw. Canonicalization and placeholder rejection are
// the server's job so PXE, FOS and the agent obey one rule set.
package smbios

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Identity is the raw SMBIOS identity tuple.
type Identity struct {
	SystemUUID   string `json:"system_uuid"`
	SystemSerial string `json:"system_serial"`
	BoardSerial  string `json:"board_serial"`
	ChassisAsset string `json:"chassis_asset"`
}

// Structure type codes from the SMBIOS spec.
const (
	typeSystem    = 1
	typeBaseboard = 2
	typeChassis   = 3
	typeEnd       = 127
)

// Parse walks a raw SMBIOS structure table. major/minor is the SMBIOS
// version from the entry point (or Windows' RSMB header); it decides the
// UUID byte order: from 2.6 on, the first three fields are little-endian
// and must be swapped to print in the RFC 4122 order dmidecode and iPXE
// use. Getting this wrong makes the agent disagree with the value FOG
// already stored for the same machine.
func Parse(table []byte, major, minor int) (Identity, error) {
	var id Identity
	swapUUID := major > 2 || (major == 2 && minor >= 6)
	off := 0
	for off+4 <= len(table) {
		typ := table[off]
		length := int(table[off+1])
		if length < 4 || off+length > len(table) {
			return id, fmt.Errorf("smbios: bad structure length %d at offset %d", length, off)
		}
		formatted := table[off : off+length]
		strs, next, err := stringSet(table, off+length)
		if err != nil {
			return id, err
		}
		switch typ {
		case typeSystem:
			if length >= 0x19 {
				id.SystemUUID = formatUUID(formatted[0x08:0x18], swapUUID)
			}
			id.SystemSerial = str(strs, formatted, 0x07)
		case typeBaseboard:
			id.BoardSerial = str(strs, formatted, 0x07)
		case typeChassis:
			id.ChassisAsset = str(strs, formatted, 0x08)
		case typeEnd:
			return id, nil
		}
		off = next
	}
	return id, nil
}

// stringSet reads the double-NUL-terminated string set that follows a
// formatted section and returns the strings plus the offset of the next
// structure.
func stringSet(table []byte, off int) ([]string, int, error) {
	var strs []string
	for {
		if off >= len(table) {
			return nil, 0, errors.New("smbios: unterminated string set")
		}
		if table[off] == 0 {
			// An empty set is "\0\0", so skip both. (A populated set ends
			// with the last string's NUL plus one more, handled below.)
			// Found on a VirtualBox guest: consuming one byte here put the
			// walk one byte off and it failed at the first string-less
			// structure.
			if off+1 >= len(table) || table[off+1] != 0 {
				return nil, 0, errors.New("smbios: bad empty string set")
			}
			return strs, off + 2, nil
		}
		end := off
		for end < len(table) && table[end] != 0 {
			end++
		}
		if end >= len(table) {
			return nil, 0, errors.New("smbios: unterminated string")
		}
		strs = append(strs, string(table[off:end]))
		off = end + 1
		if off < len(table) && table[off] == 0 {
			return strs, off + 1, nil
		}
	}
}

// str resolves the 1-based string index stored at formatted[at].
func str(strs []string, formatted []byte, at int) string {
	if at >= len(formatted) {
		return ""
	}
	n := int(formatted[at])
	if n == 0 || n > len(strs) {
		return ""
	}
	return strs[n-1]
}

// formatUUID prints 16 bytes as 8-4-4-4-12. An all-zero or all-0xFF UUID
// is "not set" per the spec; it is returned as-is so the server's
// placeholder rules see it.
func formatUUID(b []byte, swap bool) string {
	var (
		a = binary.BigEndian.Uint32(b[0:4])
		c = binary.BigEndian.Uint16(b[4:6])
		d = binary.BigEndian.Uint16(b[6:8])
	)
	if swap {
		a = binary.LittleEndian.Uint32(b[0:4])
		c = binary.LittleEndian.Uint16(b[4:6])
		d = binary.LittleEndian.Uint16(b[6:8])
	}
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		a, c, d, b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
