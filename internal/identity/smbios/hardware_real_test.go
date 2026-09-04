package smbios

import (
	"os"
	"testing"
)

// The table in testdata is the real DMI blob VirtualBox 7 hands a guest,
// captured from /sys/firmware/dmi/tables/DMI on the lab VM on 2026-09-04.
//
// The synthetic tables in hardware_test.go are built from the same reading of
// the spec as the parser, so the two can agree on a misreading and both be
// wrong. This one was emitted by firmware that has never seen this code.
//
// Nothing here is private: it is a throwaway lab VM, and the only identifier
// in it is that VM's own UUID.
func TestParseHardwareOnARealHypervisorTable(t *testing.T) {
	table, err := os.ReadFile("testdata/virtualbox-7.dmi")
	if err != nil {
		t.Fatal(err)
	}
	hw, err := ParseHardware(table, 2, 5)
	if err != nil {
		t.Fatal(err)
	}

	// Every value below was produced independently by the LINUX gatherer,
	// which reads the decoded text files under /sys/class/dmi/id rather than
	// this byte table, and was read back out of the FOG inventory row for
	// this host. Two unrelated paths, one machine, same answers.
	for _, tc := range []struct{ field, got, want string }{
		{"SysMan", hw.SysMan, "innotek GmbH"},
		{"SysProduct", hw.SysProduct, "VirtualBox"},
		{"SysSerial", hw.SysSerial, "VirtualBox-66cf2957-8c6e-4f49-a3f7-d0ab5b0f09e5"},
		{"BIOSVendor", hw.BIOSVendor, "innotek GmbH"},
		{"BIOSVersion", hw.BIOSVersion, "VirtualBox"},
		{"BIOSDate", hw.BIOSDate, "12/01/2006"},
		{"MBMan", hw.MBMan, "Oracle Corporation"},
		{"MBProductName", hw.MBProductName, "VirtualBox"},
		{"MBVersion", hw.MBVersion, "1.2"},
		{"MBSerial", hw.MBSerial, "0"},
		{"CaseMan", hw.CaseMan, "Oracle Corporation"},
		// Chassis type 1 is "Other"; the Linux row stored "Other" after the
		// same chassisTypeName mapping the Windows gatherer applies.
		{"CaseType", hw.CaseType, "1"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

// This is the gap that sent the Windows gatherer to the registry and to
// GlobalMemoryStatusEx. SMBIOS types 4 and 17 are optional and VirtualBox
// omits them entirely -- this table holds types 0, 1, 2, 3, 11 and no more --
// so a Windows guest inventoried from SMBIOS alone reports no processor and
// no memory at all. Real firmware does emit them, which is why only a VM
// could show this.
//
// Kept as its own test so that a change making ParseHardware invent a value
// from somewhere else fails here, loudly, rather than quietly making the
// fallback in gather_windows.go dead code.
func TestParseHardwareLeavesAbsentStructuresEmpty(t *testing.T) {
	table, err := os.ReadFile("testdata/virtualbox-7.dmi")
	if err != nil {
		t.Fatal(err)
	}
	hw, err := ParseHardware(table, 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ field, got string }{
		{"CPUMan", hw.CPUMan}, {"CPUVersion", hw.CPUVersion},
		{"CPUCurrent", hw.CPUCurrent}, {"CPUMax", hw.CPUMax},
		{"Mem", hw.Mem},
	} {
		if tc.got != "" {
			t.Errorf("%s = %q; a structure the firmware never emitted must stay empty, "+
				"so the platform gatherer can tell it needs its fallback", tc.field, tc.got)
		}
	}
}
