package inventory

import "testing"

func TestHashIsStableAndMoves(t *testing.T) {
	a := Inventory{SysMan: "Dell Inc.", SysSerial: "ABC123", Mem: "31924"}
	same := Inventory{SysMan: "Dell Inc.", SysSerial: "ABC123", Mem: "31924"}
	if a.Hash() != same.Hash() {
		t.Error("identical snapshots must hash the same, or the agent resends every poll")
	}
	// A disk swap has to be noticed, or the row goes stale forever.
	changed := a
	changed.HDSerial = "NEWDISK"
	if a.Hash() == changed.Hash() {
		t.Error("a changed field must move the hash")
	}
}

func TestEmptyInventoryHashIsStable(t *testing.T) {
	if (Inventory{}).Hash() != (Inventory{}).Hash() {
		t.Error("the zero value must hash consistently")
	}
}
