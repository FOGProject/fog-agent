package netboot

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
)

// The three fixtures below are REAL EFI_LOAD_OPTION bytes, read from
// /sys/firmware/efi/efivars on a Dell Precision 7550 on 2026-09-05, with the
// efivarfs attribute word already stripped the way readVar strips it. Two
// identifying values were substituted -- the NIC MAC and the disk's
// partition GUID -- and nothing else was touched, so every field this
// package parses is exactly what that firmware emitted.
//
// Taking them from the firmware rather than writing them by hand is the
// point. A fixture built from my own reading of the spec would agree with
// the parser about any mistake they shared.
const (
	// "Windows Boot Manager": a hard-drive path, no messaging node. Its
	// OptionalData carries a Windows BCD blob that CONTAINS ITS OWN
	// 7fff0400 end-of-path node, which is why FilePathListLength has to
	// bound the walk. A parser that scanned the whole variable would read
	// past the device path into that blob.
	fixtureWindows = "010000007400570069006e0064006f0077007300200042006f006f00740020004d0061006e0061006700650072000000" +
		"04012a0001000000000800000000000000c01200000000000900112233445566778899aabbccddee0202040446005c00" +
		"4500460049005c004d006900630072006f0073006f00660074005c0042006f006f0074005c0062006f006f0074006d00" +
		"6700660077002e0065006600690000007fff040057494e444f5753000100000088000000780000004200430044004f00" +
		"42004a004500430054003d007b00390064006500610038003600320063002d0035006300640064002d00340065003700" +
		"30002d0061006300630031002d006600330032006200330034003400640034003700390035007d000000300001000000" +
		"10000000040000007fff0400"

	// "Onboard NIC(IPV4)": active, MAC node (03/0b) then IPv4 node (03/0c).
	fixtureNICv4 = "01000000d4004f006e0062006f0061007200640020004e0049004300280049005000560034002900000002010c00d041" +
		"030a0000000001010600061f030b25000200000000010000000000000000000000000000000000000000000000000000" +
		"00030c1b0000000000000000000000000000000000000000000000007fff040001047a00ef47642dc93ba041ac194d51" +
		"d01b4ce650005800450020004900500076003400200049006e00740065006c0028005200290020004500740068006500" +
		"72006e0065007400200043006f006e006e0065006300740069006f006e00200028003100310029002000490032003100" +
		"39002d004c004d0000007fff04000000424f"

	// "UEFI VBOX HARDDISK ...": a VirtualBox EFI guest's disk entry, read
	// from that VM's NVRAM on 2026-09-05. Its device path contains a
	// MESSAGING node -- type 0x03, subtype 0x12, SATA -- which is exactly
	// the case that separates "this path has a messaging node" from "this
	// path is a network boot". A check written the lazy way would arm the
	// hard disk.
	fixtureVBoxSATA = "01000000200055004500460049002000560042004f005800200048004100520044004400490053004b002000560042006" +
		"30065003000380030003800340061002d00340039003100360062003700310038002000000002010c00d041030a00000000010106000" +
		"00d03120a000000ffff00007fff04004eac0881119f594d850ee21a522c59b2"

	// "Onboard NIC(IPV6)": a network path exactly like the one above, but
	// its attributes are 00000000 -- LOAD_OPTION_ACTIVE is CLEAR. The
	// firmware will not boot it, so neither may we.
	fixtureNICv6Inactive = "00000000f5004f006e0062006f0061007200640020004e0049004300280049005000560036002900000002010c00d041" +
		"030a0000000001010600061f030b25000200000000010000000000000000000000000000000000000000000000000000" +
		"00030d3c0000000000000000000000000000000000000000000000000000000000000000000000000000000040000000" +
		"000000000000000000000000007fff040001047a00ef47642dc93ba041ac194d51d01b4ce65000580045002000490050" +
		"0076003600200049006e00740065006c002800520029002000450074006800650072006e0065007400200043006f006e" +
		"006e0065006300740069006f006e0020002800310031002900200049003200310039002d004c004d0000007fff040000" +
		"00424f"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return b
}

// activate returns a copy of a load option with LOAD_OPTION_ACTIVE set, so
// the inactive IPv6 fixture can also serve as an active one without a
// second hand-written blob.
func activate(b []byte) []byte {
	out := append([]byte(nil), b...)
	binary.LittleEndian.PutUint32(out[0:4], loadOptionActive)
	return out
}

func TestRealFirmwareNetworkOptionIsRecognized(t *testing.T) {
	lo, err := parseLoadOption(mustHex(t, fixtureNICv4))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if lo.description != "Onboard NIC(IPV4)" {
		t.Errorf("description = %q, want %q", lo.description, "Onboard NIC(IPV4)")
	}
	if !lo.active {
		t.Error("the entry is marked active in firmware but parsed as inactive")
	}
	if !lo.network {
		t.Error("a path with a MAC node parsed as not a network boot")
	}
	if !lo.ipv4 {
		t.Error("a path with an IPv4 node parsed as not IPv4")
	}
}

func TestRealFirmwareDiskOptionIsNotANetworkBoot(t *testing.T) {
	lo, err := parseLoadOption(mustHex(t, fixtureWindows))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if lo.description != "Windows Boot Manager" {
		t.Errorf("description = %q, want %q", lo.description, "Windows Boot Manager")
	}
	if lo.network {
		// This is the OptionalData trap: the Windows BCD blob after the
		// device path contains bytes that look like messaging nodes, and
		// reaching them means FilePathListLength was ignored.
		t.Error("a hard-drive boot entry parsed as a network boot; the walk ran past FilePathListLength")
	}
}

// macNode is a well-formed MAC messaging node: type 0x03, subtype 0x0b,
// length 0x0025, then 33 bytes of payload. Byte-identical in shape to the
// one in the real NIC fixtures above.
func macNode() []byte {
	n := make([]byte, 0x25)
	n[0], n[1] = dpTypeMessaging, dpMsgMACAddr
	binary.LittleEndian.PutUint16(n[2:4], 0x25)
	copy(n[4:], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
	return n
}

// TestOptionalDataIsNotPartOfTheDevicePath is the test that actually holds
// FilePathListLength in place.
//
// The obvious version of this test -- the real "Windows Boot Manager"
// fixture, whose OptionalData carries a BCD blob -- passes whether or not
// the length is honoured, because that device path ends with a proper
// 7fff0400 node and the walk stops there regardless. It proved nothing, and
// a mutation removing the bound survived it.
//
// This entry is built so the two differ: its device path has NO end node,
// so an unbounded walk runs straight out of the declared region and into
// OptionalData, where a MAC node is waiting. Synthetic, and deliberately
// so: it is the shape that separates the two implementations, not a shape
// any particular firmware was observed emitting.
func TestOptionalDataIsNotPartOfTheDevicePath(t *testing.T) {
	desc := utf16.Encode([]rune("Odd Entry"))
	// One ACPI node, 12 bytes, and no end-of-path node after it.
	path := make([]byte, 12)
	path[0], path[1] = 0x02, 0x01
	binary.LittleEndian.PutUint16(path[2:4], 12)

	b := make([]byte, 6)
	binary.LittleEndian.PutUint32(b[0:4], loadOptionActive)
	binary.LittleEndian.PutUint16(b[4:6], uint16(len(path)))
	for _, u := range desc {
		var x [2]byte
		binary.LittleEndian.PutUint16(x[:], u)
		b = append(b, x[:]...)
	}
	b = append(b, 0, 0)
	b = append(b, path...)
	b = append(b, macNode()...) // OptionalData: off limits

	lo, err := parseLoadOption(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if lo.network {
		t.Error("the walk read past FilePathListLength into OptionalData and found a MAC node there")
	}
}

// The end-of-path node ends the path even when FilePathListLength declares
// more bytes after it. Firmware is free to pad the declared region, and
// anything past the terminator is not part of the path -- so a MAC node
// sitting in that padding must not count.
//
// Without this, the 7fff check is unreachable by any test: inside a
// well-formed path the terminator is the last node anyway, and the walk
// would end on its own when the bytes ran out.
func TestNodesAfterTheEndOfPathNodeAreNotPartOfTheDevicePath(t *testing.T) {
	acpi := make([]byte, 12)
	acpi[0], acpi[1] = 0x02, 0x01
	binary.LittleEndian.PutUint16(acpi[2:4], 12)

	end := []byte{dpTypeEnd, 0xff, 0x04, 0x00}

	dp := append(append(append([]byte{}, acpi...), end...), macNode()...)
	if network, _ := devicePathIsNetwork(dp); network {
		t.Error("a MAC node in the padding after the end-of-path node was treated as part of the path")
	}
}

// Not every messaging node is a network node. A SATA disk hangs off one
// too, and arming it would boot the machine to the very disk this whole
// design exists to avoid.
func TestAMessagingNodeThatIsNotANetworkNodeDoesNotCount(t *testing.T) {
	lo, err := parseLoadOption(mustHex(t, fixtureVBoxSATA))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.HasPrefix(lo.description, "UEFI VBOX HARDDISK") {
		t.Errorf("description = %q", lo.description)
	}
	if lo.network {
		t.Error("a SATA messaging node was treated as a network boot")
	}
}

func TestInactiveEntryIsNotACandidate(t *testing.T) {
	lo, err := parseLoadOption(mustHex(t, fixtureNICv6Inactive))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !lo.network {
		t.Fatal("fixture is meant to be a network path")
	}
	if lo.active {
		t.Fatal("fixture is meant to have LOAD_OPTION_ACTIVE clear")
	}
	// choose must skip it even though it is the only network entry there.
	if _, ok := choose([]uint16{4}, map[uint16]loadOption{4: lo}); ok {
		t.Error("choose armed an entry the firmware has marked inactive")
	}
}

func TestIPv4WinsOverIPv6EvenWhenBootOrderDisagrees(t *testing.T) {
	v4, err := parseLoadOption(mustHex(t, fixtureNICv4))
	if err != nil {
		t.Fatal(err)
	}
	v6, err := parseLoadOption(activate(mustHex(t, fixtureNICv6Inactive)))
	if err != nil {
		t.Fatal(err)
	}
	// BootOrder puts the IPv6 entry first. FOG serves PXE over IPv4, so
	// arming the IPv6 entry would send the machine looking for a server
	// that is not there.
	got, ok := choose([]uint16{8, 3}, map[uint16]loadOption{8: v6, 3: v4})
	if !ok {
		t.Fatal("no option chosen")
	}
	if got.Number != 3 {
		t.Errorf("chose Boot%04X (%s), want the IPv4 entry Boot0003", got.Number, got.Description)
	}
}

func TestIPv6IsUsedWhenItIsTheOnlyNetworkEntry(t *testing.T) {
	v6, err := parseLoadOption(activate(mustHex(t, fixtureNICv6Inactive)))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := choose([]uint16{8}, map[uint16]loadOption{8: v6})
	if !ok {
		t.Fatal("an active network entry was rejected because it is IPv6")
	}
	if got.Number != 8 || got.IPv4 {
		t.Errorf("got %v ipv4=%t, want Boot0008 ipv4=false", got, got.IPv4)
	}
}

func TestBootOrderIsHonouredAmongEqualEntries(t *testing.T) {
	v6, err := parseLoadOption(activate(mustHex(t, fixtureNICv6Inactive)))
	if err != nil {
		t.Fatal(err)
	}
	opts := map[uint16]loadOption{8: v6, 9: v6}
	for _, tc := range []struct {
		order []uint16
		want  uint16
	}{
		{[]uint16{8, 9}, 8},
		{[]uint16{9, 8}, 9},
	} {
		got, ok := choose(tc.order, opts)
		if !ok || got.Number != tc.want {
			t.Errorf("order %v: got Boot%04X ok=%t, want Boot%04X", tc.order, got.Number, ok, tc.want)
		}
	}
}

func TestAMalformedOptionIsAnErrorNotANonNetworkAnswer(t *testing.T) {
	full := mustHex(t, fixtureNICv4)
	cases := []struct {
		name string
		in   []byte
	}{
		{"shorter than the header", full[:4]},
		{"description never terminates", func() []byte {
			// Strip the NUL that ends the description so the scan runs off
			// the end.
			b := append([]byte(nil), full...)
			for i := 6; i+1 < len(b); i += 2 {
				if b[i] == 0 && b[i+1] == 0 {
					b[i] = 'x'
				}
			}
			return b
		}()},
		{"device path longer than the bytes present", func() []byte {
			b := append([]byte(nil), full...)
			binary.LittleEndian.PutUint16(b[4:6], 0xfff0)
			return b
		}()},
	}
	for _, tc := range cases {
		if _, err := parseLoadOption(tc.in); err == nil {
			// Silently answering "not a network boot" here would report a
			// machine that CAN netboot as one that cannot, and the admin
			// would have no way to tell that from the truth.
			t.Errorf("%s: parsed without error", tc.name)
		}
	}
}

func TestATruncatedDevicePathNodeStopsTheWalk(t *testing.T) {
	// A node claiming to be longer than what remains, and a node claiming
	// to be shorter than its own 4-byte header, must both terminate the
	// walk rather than loop or read out of bounds.
	for _, dp := range [][]byte{
		{0x03, 0x0b, 0xff, 0xff},
		{0x03, 0x0b, 0x00, 0x00},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("device path %x panicked: %v", dp, r)
				}
			}()
			devicePathIsNetwork(dp)
		}()
	}
}

// fakeFirmware stands in for the platform readVar during Find().
type fakeFirmware struct {
	vars map[string][]byte
	err  error
}

func (f fakeFirmware) read(name string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.vars[name]
	if !ok {
		return nil, fmt.Errorf("no variable %s", name)
	}
	return b, nil
}

func bootOrder(nums ...uint16) []byte {
	b := make([]byte, 0, len(nums)*2)
	for _, n := range nums {
		var x [2]byte
		binary.LittleEndian.PutUint16(x[:], n)
		b = append(b, x[:]...)
	}
	return b
}

func withFirmware(t *testing.T, f fakeFirmware) {
	t.Helper()
	old := readVar
	readVar = f.read
	t.Cleanup(func() { readVar = old })
}

// This is the machine the fixtures came from: BootOrder 0001,0000,0003,0008,
// 0002, where 0003 is the active IPv4 NIC entry.
func TestFindPicksTheNetworkEntryOutOfARealBootOrder(t *testing.T) {
	withFirmware(t, fakeFirmware{vars: map[string][]byte{
		"BootOrder": bootOrder(1, 0, 3, 8, 2),
		"Boot0000":  mustHex(t, fixtureWindows),
		"Boot0001":  mustHex(t, fixtureWindows),
		"Boot0002":  mustHex(t, fixtureWindows),
		"Boot0003":  mustHex(t, fixtureNICv4),
		"Boot0008":  activate(mustHex(t, fixtureNICv6Inactive)),
	}})
	got, err := Find()
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Number != 3 {
		t.Errorf("Find chose %v, want Boot0003", got)
	}
}

func TestFindReportsNoOptionWhenNothingNetboots(t *testing.T) {
	// A machine whose firmware lists only disk entries. This is the
	// VirtualBox case measured in design 0013: the Boot Manager offers
	// "UEFI PXEv4" interactively but persists no Boot#### for it, so
	// BootNext has nothing to point at and the agent must not reboot.
	withFirmware(t, fakeFirmware{vars: map[string][]byte{
		"BootOrder": bootOrder(0, 1),
		"Boot0000":  mustHex(t, fixtureWindows),
		"Boot0001":  mustHex(t, fixtureWindows),
	}})
	_, err := Find()
	if !errors.Is(err, ErrNoOption) {
		t.Errorf("Find error = %v, want ErrNoOption", err)
	}
}

func TestFindSurfacesUnsupportedRatherThanNoOption(t *testing.T) {
	// A BIOS machine and a UEFI machine with PXE switched off are
	// different problems with different fixes, so they must not collapse
	// into one error.
	withFirmware(t, fakeFirmware{err: ErrUnsupported})
	_, err := Find()
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Find error = %v, want ErrUnsupported", err)
	}
	if errors.Is(err, ErrNoOption) {
		t.Error("a machine with no UEFI was reported as one with no network entry")
	}
}

func TestFindSkipsAnEntryBootOrderNamesButFirmwareDoesNotHold(t *testing.T) {
	// Firmware is free to list an option number it no longer stores. That
	// must not stop the others being considered.
	withFirmware(t, fakeFirmware{vars: map[string][]byte{
		"BootOrder": bootOrder(7, 3),
		"Boot0003":  mustHex(t, fixtureNICv4),
	}})
	got, err := Find()
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Number != 3 {
		t.Errorf("Find chose %v, want Boot0003", got)
	}
}

func TestArmWritesTheOptionNumberLittleEndian(t *testing.T) {
	var gotName string
	var gotData []byte
	old := writeVar
	writeVar = func(name string, data []byte) error {
		gotName, gotData = name, append([]byte(nil), data...)
		return nil
	}
	t.Cleanup(func() { writeVar = old })

	if err := Arm(Option{Number: 0x0103}); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if gotName != "BootNext" {
		t.Errorf("wrote %q, want BootNext", gotName)
	}
	// Two bytes, low byte first. Writing 0x0103 as {01,03} would arm
	// Boot0301, which is a different entry or none at all.
	if len(gotData) != 2 || gotData[0] != 0x03 || gotData[1] != 0x01 {
		t.Errorf("wrote % x, want 03 01", gotData)
	}
}

// A description is only ever shown to a person. Pinning it here keeps the
// UTF-16 decode honest, since every other assertion in this file would pass
// with a garbled string.
func TestDescriptionsAreDecodedAsUTF16(t *testing.T) {
	want := "Onboard NIC(IPV4)"
	lo, err := parseLoadOption(mustHex(t, fixtureNICv4))
	if err != nil {
		t.Fatal(err)
	}
	if lo.description != want {
		t.Fatalf("description = %q, want %q", lo.description, want)
	}
	if got := string(utf16.Decode(utf16.Encode([]rune(want)))); got != lo.description {
		t.Errorf("round trip mismatch: %q", got)
	}
}
