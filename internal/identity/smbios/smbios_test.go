package smbios

import "testing"

// table builds a minimal SMBIOS table: one System (type 1), one Baseboard
// (type 2), one Chassis (type 3), then End (type 127).
func table() []byte {
	var t []byte
	// Type 1, length 0x1B: manufacturer=1 product=2 version=3 serial=4, UUID at 0x08.
	sys := []byte{1, 0x1B, 0, 0, 1, 2, 3, 4}
	uuid := []byte{0x67, 0x45, 0x23, 0x01, 0xab, 0x89, 0xef, 0xcd, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	sys = append(sys, uuid...)
	sys = append(sys, 6, 0, 0) // wakeup, sku, family
	t = append(t, sys...)
	t = append(t, "Dell\x00Precision\x00v1\x007XPOC01\x00\x00"...)
	// Type 2, length 0x0F: manufacturer=1 product=2 version=0 serial=3.
	t = append(t, 2, 0x0F, 1, 0, 1, 2, 0, 3, 0, 0, 0, 0, 0, 0, 0)
	t = append(t, "Dell\x00Board\x00/7XPOC01/CN1234567890AB/\x00\x00"...)
	// Type 3, length 0x0D: manufacturer=1 type version=0 serial=0 asset=2 at 0x08.
	t = append(t, 3, 0x0D, 2, 0, 1, 3, 0, 0, 2, 0, 0, 0, 0)
	t = append(t, "Dell\x0070638948\x00\x00"...)
	// Type 32 (system boot), no strings: the "\x00\x00" must be skipped
	// whole or every structure after it is misread.
	t = append(t, 32, 0x0B, 4, 0, 0, 0, 0, 0, 0, 0, 0)
	t = append(t, 0, 0)
	t = append(t, 127, 4, 3, 0, 0, 0)
	return t
}

func TestParseModern(t *testing.T) {
	id, err := Parse(table(), 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{
		SystemUUID:   "01234567-89ab-cdef-0123-456789abcdef",
		SystemSerial: "7XPOC01",
		BoardSerial:  "/7XPOC01/CN1234567890AB/",
		ChassisAsset: "70638948",
	}
	if id != want {
		t.Fatalf("got %+v\nwant %+v", id, want)
	}
}

// Pre-2.6 tables store the UUID in network order: the same bytes print
// without the swap.
func TestParseLegacyUUIDOrder(t *testing.T) {
	id, err := Parse(table(), 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if id.SystemUUID != "67452301-ab89-efcd-0123-456789abcdef" {
		t.Fatalf("legacy uuid = %s", id.SystemUUID)
	}
}

func TestParseTruncated(t *testing.T) {
	if _, err := Parse(table()[:20], 3, 2); err == nil {
		t.Fatal("expected error on truncated table")
	}
}
