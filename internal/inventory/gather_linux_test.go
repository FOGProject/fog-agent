//go:build linux

package inventory

import "testing"

// A trimmed /proc/cpuinfo: two cores, so the parser must take the first
// block's values and not append the repeats.
const cpuinfoFixture = `processor	: 0
vendor_id	: GenuineIntel
cpu family	: 6
model name	: Intel(R) Core(TM) i7-10850H CPU @ 2.70GHz
cpu MHz		: 2712.003
processor	: 1
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7-10850H CPU @ 2.70GHz
cpu MHz		: 1200.117
`

func TestParseCPUInfoTakesTheFirstBlock(t *testing.T) {
	vendor, model, current := parseCPUInfo(cpuinfoFixture)
	if vendor != "GenuineIntel" {
		t.Errorf("vendor = %q, want GenuineIntel", vendor)
	}
	if model != "Intel(R) Core(TM) i7-10850H CPU @ 2.70GHz" {
		t.Errorf("model = %q", model)
	}
	// Rounded to whole MHz: a fractional value would move the inventory
	// hash on every poll and resend the block forever.
	if current != "2712" {
		t.Errorf("current = %q, want 2712", current)
	}
}

func TestParseCPUInfoEmpty(t *testing.T) {
	vendor, model, current := parseCPUInfo("")
	if vendor != "" || model != "" || current != "" {
		t.Errorf("got %q/%q/%q, want all empty", vendor, model, current)
	}
}

func TestParseMemTotalMB(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"kB to MB", "MemTotal:       32690832 kB\nMemFree: 100 kB\n", "31924"},
		{"absent", "MemFree:  100 kB\n", ""},
		{"unparsable", "MemTotal:       notanumber kB\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseMemTotalMB(tc.in); got != tc.want {
				t.Errorf("parseMemTotalMB() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKhzToMHz(t *testing.T) {
	if got := khzToMHz("2700000"); got != "2700" {
		t.Errorf("got %q, want 2700", got)
	}
	if got := khzToMHz(""); got != "" {
		t.Errorf("empty input should stay empty, got %q", got)
	}
}

func TestChassisTypeNameKeepsUnknownAsNumber(t *testing.T) {
	if got := chassisTypeName("10"); got != "Notebook" {
		t.Errorf("got %q, want Notebook", got)
	}
	// An unmapped code must stay the raw number rather than become a
	// plausible-looking lie.
	if got := chassisTypeName("99"); got != "99" {
		t.Errorf("got %q, want 99", got)
	}
}

func TestParseLspciMMPicksDisplayControllers(t *testing.T) {
	out := `00:02.0 "VGA compatible controller" "Intel Corporation" "CometLake-H GT2 [UHD Graphics]" -r02 "Dell" "Device 097d"
00:1f.3 "Audio device" "Intel Corporation" "Comet Lake PCH cAVS" -r00 "Dell" "Device 097d"
01:00.0 "3D controller" "NVIDIA Corporation" "TU117GLM [Quadro T1000 Mobile]" -ra1 "Dell" "Device 097d"
`
	vendors, products := parseLspciMM(out)
	if len(vendors) != 2 || len(products) != 2 {
		t.Fatalf("got %d vendors / %d products, want 2 each: %v %v", len(vendors), len(products), vendors, products)
	}
	if vendors[0] != "Intel Corporation" || vendors[1] != "NVIDIA Corporation" {
		t.Errorf("vendors = %v", vendors)
	}
	if products[1] != "TU117GLM [Quadro T1000 Mobile]" {
		t.Errorf("products = %v", products)
	}
}

func TestIsVirtualBlockDevice(t *testing.T) {
	for _, name := range []string{"loop0", "ram1", "zram0", "sr0", "dm-0", "md127", "nbd3"} {
		if !isVirtualBlockDevice(name) {
			t.Errorf("%q should be virtual", name)
		}
	}
	for _, name := range []string{"sda", "nvme0n1", "vda", "hda"} {
		if isVirtualBlockDevice(name) {
			t.Errorf("%q should be a real disk", name)
		}
	}
}
