package inventory

import "testing"

// Every id here is one the lab host actually reported on 2026-09-04, or the
// documented form for a platform FOG has to keep working on. The point of the
// test is the asymmetry: dropping a real adapter is a silent wrong answer (a
// machine that looks like it has no GPU), so anything unrecognized is kept.
func TestPhysicalAdapterKeepsRealHardwareAndDropsIndirectDisplays(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
		want bool
	}{
		// Observed on telliottwin11: one real adapter, three RDP pseudo-devices.
		{"VirtualBox GPU on PCI", `PCI\VEN_80EE&DEV_BEEF`, true},
		{"RDP indirect display", "RdpIdd_IndirectDisplay", false},
		{"indirect display, odd casing", "rdpIDD_IndirectDISPLAY", false},

		// Real adapters that a PCI-only allow-list would have dropped. These
		// are the reason the rule is an exclusion.
		{"Hyper-V synthetic video on VMBUS", `VMBUS\{da0a7802-e377-4aac-8e77-0558eb1073f8}`, true},
		{"USB dock adapter", `USB\VID_17E9&PID_4307`, true},
		{"Intel integrated", `PCI\VEN_8086&DEV_9BC4`, true},

		// A software root-enumerated device is not display hardware.
		{"root-enumerated software device", `ROOT\BasicDisplay`, false},
		{"root, lowercase", `root\basicrender`, false},

		// Fail open: an id we do not recognize, or none at all, is kept
		// rather than silently dropping a machine's only GPU.
		{"unknown bus", `SOMEBUS\WHATEVER`, true},
		{"empty id", "", true},
		{"whitespace only", "   ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := physicalAdapter(tc.id); got != tc.want {
				t.Errorf("physicalAdapter(%q) = %t, want %t", tc.id, got, tc.want)
			}
		})
	}
}
