package smbios

import (
	"encoding/binary"
	"testing"
)

// structure assembles one SMBIOS structure: the formatted section as given,
// followed by its string set. Building tables byte by byte rather than
// capturing one from a machine is the point -- a captured table only proves
// the parser agrees with itself about the machine it came from, and the
// fields that matter here (an empty CPU socket, a 0x7FFF extended memory
// size) are exactly the ones no one machine has all of.
func structure(typ byte, formatted []byte, strs ...string) []byte {
	body := make([]byte, len(formatted)+4)
	body[0] = typ
	body[1] = byte(len(formatted) + 4)
	binary.LittleEndian.PutUint16(body[2:4], 0x0100) // handle, unread
	copy(body[4:], formatted)
	for _, s := range strs {
		body = append(body, []byte(s)...)
		body = append(body, 0)
	}
	if len(strs) == 0 {
		// An empty string set is two NULs, not one.
		body = append(body, 0)
	}
	return append(body, 0)
}

// pad returns a formatted section of n bytes past the header, so offsets in
// the test line up with the ones the spec (and the parser) use.
func pad(n int) []byte { return make([]byte, n) }

func TestParseHardwareReadsEachStructure(t *testing.T) {
	// Type 0: vendor@0x04, version@0x05, date@0x08.
	bios := pad(0x0B - 4)
	bios[0x04-4], bios[0x05-4], bios[0x08-4] = 1, 2, 3

	// Type 1: man@0x04 product@0x05 version@0x06 serial@0x07.
	sys := pad(0x19 - 4)
	sys[0x04-4], sys[0x05-4], sys[0x06-4], sys[0x07-4] = 1, 2, 3, 4

	// Type 2: man@0x04 product@0x05 version@0x06 serial@0x07 asset@0x08.
	board := pad(0x0F - 4)
	board[0x04-4], board[0x05-4], board[0x06-4], board[0x07-4], board[0x08-4] = 1, 2, 3, 4, 5

	// Type 3: man@0x04, type byte@0x05 with the lock bit set, ver@0x06,
	// serial@0x07, asset@0x08.
	chassis := pad(0x15 - 4)
	chassis[0x04-4] = 1
	chassis[0x05-4] = 0x80 | 10 // lock flag + Notebook
	chassis[0x06-4], chassis[0x07-4], chassis[0x08-4] = 2, 3, 4

	// Type 4: a populated central processor at 2700/5100 MHz.
	cpu := pad(0x1A - 4)
	cpu[0x05-4] = 3    // Central Processor
	cpu[0x07-4] = 1    // manufacturer string
	cpu[0x10-4] = 2    // version string
	cpu[0x18-4] = 0x40 // socket populated
	binary.LittleEndian.PutUint16(cpu[0x14-4:0x16-4], 5100)
	binary.LittleEndian.PutUint16(cpu[0x16-4:0x18-4], 2700)

	// Two 16384 MB modules, size in MB (bit 15 clear).
	mem := pad(0x15 - 4)
	binary.LittleEndian.PutUint16(mem[0x0C-4:0x0E-4], 16384)

	var table []byte
	table = append(table, structure(0, bios, "Dell Inc.", "1.45.0", "06/29/2026")...)
	table = append(table, structure(1, sys, "Dell Inc.", "Precision 7550", "Not Specified", "ABC1234")...)
	table = append(table, structure(2, board, "Dell Inc.", "0G7VJK", "A00", "/ABC1234/", "asset-1")...)
	table = append(table, structure(3, chassis, "Dell Inc.", "chassis-ver", "chassis-sn", "chassis-asset")...)
	table = append(table, structure(4, cpu, "GenuineIntel", "Intel(R) Core(TM) i7-10850H CPU @ 2.70GHz")...)
	table = append(table, structure(17, mem)...)
	table = append(table, structure(17, mem)...)
	table = append(table, structure(127, pad(0))...)

	hw, err := ParseHardware(table, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ field, got, want string }{
		{"BIOSVendor", hw.BIOSVendor, "Dell Inc."},
		{"BIOSVersion", hw.BIOSVersion, "1.45.0"},
		{"BIOSDate", hw.BIOSDate, "06/29/2026"},
		{"SysProduct", hw.SysProduct, "Precision 7550"},
		{"SysSerial", hw.SysSerial, "ABC1234"},
		{"MBProductName", hw.MBProductName, "0G7VJK"},
		{"MBAsset", hw.MBAsset, "asset-1"},
		// The lock bit must be masked off, or a locked notebook reports
		// chassis type 138 and renders as the raw number.
		{"CaseType", hw.CaseType, "10"},
		{"CaseAsset", hw.CaseAsset, "chassis-asset"},
		{"CPUMan", hw.CPUMan, "GenuineIntel"},
		{"CPUMax", hw.CPUMax, "5100"},
		{"CPUCurrent", hw.CPUCurrent, "2700"},
		{"Mem", hw.Mem, "32768"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

func TestParseHardwareSkipsEmptyCPUSocket(t *testing.T) {
	empty := pad(0x1A - 4)
	empty[0x05-4] = 3 // says Central Processor
	empty[0x10-4] = 1 // and has a version string
	empty[0x18-4] = 0 // but the socket is not populated
	binary.LittleEndian.PutUint16(empty[0x16-4:0x18-4], 1)

	real := pad(0x1A - 4)
	real[0x05-4], real[0x07-4], real[0x10-4], real[0x18-4] = 3, 1, 2, 0x40
	binary.LittleEndian.PutUint16(real[0x16-4:0x18-4], 3200)

	var table []byte
	table = append(table, structure(4, empty, "empty socket")...)
	table = append(table, structure(4, real, "GenuineIntel", "Real CPU")...)
	table = append(table, structure(127, pad(0))...)

	hw, err := ParseHardware(table, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if hw.CPUVersion != "Real CPU" {
		t.Errorf("CPUVersion = %q; an unpopulated socket must not win", hw.CPUVersion)
	}
	if hw.CPUCurrent != "3200" {
		t.Errorf("CPUCurrent = %q, want 3200", hw.CPUCurrent)
	}
}

func TestParseHardwareMemoryUnits(t *testing.T) {
	// A 32 GB module: too big for the WORD, so the size is 0x7FFF and the
	// real value is the Extended Size DWORD in MB.
	big := pad(0x20 - 4)
	binary.LittleEndian.PutUint16(big[0x0C-4:0x0E-4], 0x7FFF)
	binary.LittleEndian.PutUint32(big[0x1C-4:0x20-4], 65536)

	// A 512 KB module: bit 15 set means the value is KB, not MB. Rounding
	// this to MB per device would report 0 and lose it.
	small := pad(0x15 - 4)
	binary.LittleEndian.PutUint16(small[0x0C-4:0x0E-4], 0x8000|512)

	// An empty slot and an unknown size contribute nothing.
	none := pad(0x15 - 4)
	unknown := pad(0x15 - 4)
	binary.LittleEndian.PutUint16(unknown[0x0C-4:0x0E-4], 0xFFFF)

	var table []byte
	for _, m := range [][]byte{big, small, none, unknown} {
		table = append(table, structure(17, m)...)
	}
	table = append(table, structure(127, pad(0))...)

	hw, err := ParseHardware(table, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 65536 MB + 512 KB, floored to whole MB.
	if hw.Mem != "65536" {
		t.Errorf("Mem = %q, want 65536", hw.Mem)
	}
}

func TestParseHardwareReportsNothingRatherThanZeros(t *testing.T) {
	// A table with no memory devices and a zero-speed CPU. Empty strings,
	// not "0": the server stores what it is sent, and a 0 MHz processor on
	// the Inventory tab is a lie a blank field does not tell.
	cpu := pad(0x1A - 4)
	cpu[0x05-4], cpu[0x10-4], cpu[0x18-4] = 3, 1, 0x40

	table := append(structure(4, cpu, "A CPU"), structure(127, pad(0))...)
	hw, err := ParseHardware(table, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if hw.Mem != "" || hw.CPUMax != "" || hw.CPUCurrent != "" {
		t.Errorf("got Mem=%q CPUMax=%q CPUCurrent=%q, want all empty",
			hw.Mem, hw.CPUMax, hw.CPUCurrent)
	}
}

func TestParseHardwareRefusesABadTable(t *testing.T) {
	// A structure claiming a length shorter than its own header. Half a
	// row is worse than none: the server takes what it gets as the truth.
	if _, err := ParseHardware([]byte{1, 2, 0, 0}, 3, 3); err == nil {
		t.Error("a structure length under 4 must be an error")
	}
}
