//go:build linux

package inventory

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// dmiDir is the kernel's decoded DMI tree, the same one identity.readSMBIOS
// reads. The *_serial and chassis_asset_tag files are mode 0400, so a
// non-root run reports "" for them rather than failing the whole gather.
const dmiDir = "/sys/class/dmi/id/"

// gather reads hardware from the kernel's own views: DMI for the firmware
// identity, /proc for CPU and memory, /sys/block for the primary disk and
// /sys/bus/pci for display adapters. The only external command is lspci,
// and only to put human names on PCI ids we can otherwise report as hex.
//
// The bool is false when nothing at all could be read -- an unprivileged
// container with no DMI tree -- so the caller sends no block rather than
// blanking a good row on the server.
func gather() (Inventory, bool) {
	var inv Inventory

	inv.SysMan = dmi("sys_vendor")
	inv.SysProduct = dmi("product_name")
	inv.SysVersion = dmi("product_version")
	inv.SysSerial = dmi("product_serial")
	inv.SysUUID = dmi("product_uuid")
	inv.SysType = chassisTypeName(dmi("chassis_type"))

	inv.BIOSVendor = dmi("bios_vendor")
	inv.BIOSVersion = dmi("bios_version")
	inv.BIOSDate = dmi("bios_date")

	inv.MBMan = dmi("board_vendor")
	inv.MBProductName = dmi("board_name")
	inv.MBVersion = dmi("board_version")
	inv.MBSerial = dmi("board_serial")
	inv.MBAsset = dmi("board_asset_tag")

	inv.CaseMan = dmi("chassis_vendor")
	inv.CaseVer = dmi("chassis_version")
	inv.CaseSerial = dmi("chassis_serial")
	inv.CaseAsset = dmi("chassis_asset_tag")

	vendor, model, current := parseCPUInfo(readTrimmed("/proc/cpuinfo"))
	inv.CPUMan, inv.CPUVersion, inv.CPUCurrent = vendor, model, current
	inv.CPUMax = khzToMHz(readTrimmed("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"))
	if inv.CPUCurrent == "" {
		inv.CPUCurrent = khzToMHz(readTrimmed("/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq"))
	}

	inv.Mem = parseMemTotalMB(readTrimmed("/proc/meminfo"))

	inv.HDModel, inv.HDSerial, inv.HDFirmware = primaryDisk()
	inv.GPUVendors, inv.GPUProducts = gpus()

	return inv, inv != Inventory{}
}

// dmi reads one decoded DMI field, empty when it cannot be read.
func dmi(name string) string { return readTrimmed(dmiDir + name) }

// readTrimmed returns a file's contents without trailing newline padding,
// and "" for anything unreadable: an absent fact and an unreadable one are
// the same to the inventory row.
func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// parseCPUInfo pulls the vendor, model name and current MHz out of
// /proc/cpuinfo. Split out from the file read so it is testable against a
// fixture rather than whatever this machine happens to have.
func parseCPUInfo(content string) (vendor, model, currentMHz string) {
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "vendor_id":
			if vendor == "" {
				vendor = value
			}
		case "model name":
			if model == "" {
				model = value
			}
		case "cpu MHz":
			if currentMHz == "" {
				// Whole MHz: the fractional part is noise that would
				// change the inventory hash on every poll.
				if f, err := strconv.ParseFloat(value, 64); err == nil {
					currentMHz = strconv.FormatInt(int64(f+0.5), 10)
				}
			}
		}
		// The first processor block carries everything needed; later
		// blocks repeat it per core.
		if vendor != "" && model != "" && currentMHz != "" {
			break
		}
	}
	return vendor, model, currentMHz
}

// parseMemTotalMB converts /proc/meminfo's MemTotal (kB) to whole megabytes.
// The server's Inventory::getMem() formats it for display.
func parseMemTotalMB(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return ""
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return ""
		}
		return strconv.FormatInt(kb/1024, 10)
	}
	return ""
}

// khzToMHz converts a cpufreq value (kHz) to whole MHz.
func khzToMHz(khz string) string {
	if khz == "" {
		return ""
	}
	v, err := strconv.ParseInt(khz, 10, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(v/1000, 10)
}

// chassisTypeName turns the SMBIOS chassis type number the kernel exposes
// into the word the Inventory tab should show. An unmapped type stays the
// raw number rather than becoming a lie; the mapping is SMBIOS 3.x table 17.
func chassisTypeName(raw string) string {
	names := map[string]string{
		"1": "Other", "2": "Unknown", "3": "Desktop", "4": "Low Profile Desktop",
		"5": "Pizza Box", "6": "Mini Tower", "7": "Tower", "8": "Portable",
		"9": "Laptop", "10": "Notebook", "11": "Hand Held", "12": "Docking Station",
		"13": "All In One", "14": "Sub Notebook", "15": "Space-saving",
		"16": "Lunch Box", "17": "Main Server Chassis", "18": "Expansion Chassis",
		"21": "Peripheral Chassis", "22": "RAID Chassis", "23": "Rack Mount Chassis",
		"24": "Sealed-case PC", "25": "Multi-system Chassis", "28": "Blade",
		"29": "Blade Enclosure", "30": "Tablet", "31": "Convertible",
		"32": "Detachable", "34": "Embedded PC", "35": "Mini PC",
	}
	if name, ok := names[raw]; ok {
		return name
	}
	return raw
}

// primaryDisk picks the machine's main block device and reports its model,
// serial and firmware. The inventory table holds exactly one disk, so this
// chooses rather than concatenating: the first real, non-removable device in
// name order. Multiple disks are a normalized relation later (0007).
func primaryDisk() (model, serial, firmware string) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return "", "", ""
	}
	var names []string
	for _, e := range entries {
		if isVirtualBlockDevice(e.Name()) {
			continue
		}
		// Removable media is not this machine's disk.
		if readTrimmed(filepath.Join("/sys/block", e.Name(), "removable")) == "1" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		dev := filepath.Join("/sys/block", name, "device")
		model = readTrimmed(filepath.Join(dev, "model"))
		serial = readTrimmed(filepath.Join(dev, "serial"))
		// SATA exposes firmware as "rev", NVMe as "firmware_rev".
		firmware = readTrimmed(filepath.Join(dev, "firmware_rev"))
		if firmware == "" {
			firmware = readTrimmed(filepath.Join(dev, "rev"))
		}
		if model != "" || serial != "" {
			return model, serial, firmware
		}
	}
	return "", "", ""
}

// isVirtualBlockDevice reports whether a /sys/block name is something other
// than a physical disk: loopbacks, ram disks, optical, device-mapper, RAID
// and network block devices all have no inventory value.
func isVirtualBlockDevice(name string) bool {
	for _, prefix := range []string{"loop", "ram", "zram", "sr", "dm-", "md", "nbd", "fd"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// gpus reports display adapters as the two comma-joined strings the
// inventory table keeps. lspci gives human names; without it the PCI vendor
// and device ids are still better than nothing.
func gpus() (vendors, products string) {
	if out, err := exec.Command("lspci", "-mm").Output(); err == nil {
		v, p := parseLspciMM(string(out))
		if len(v) > 0 {
			return strings.Join(v, ","), strings.Join(p, ",")
		}
	}
	var v, p []string
	devices, err := filepath.Glob("/sys/bus/pci/devices/*")
	if err != nil {
		return "", ""
	}
	sort.Strings(devices)
	for _, dir := range devices {
		// PCI class 0x03xxxx is a display controller.
		if !strings.HasPrefix(readTrimmed(filepath.Join(dir, "class")), "0x03") {
			continue
		}
		v = append(v, readTrimmed(filepath.Join(dir, "vendor")))
		p = append(p, readTrimmed(filepath.Join(dir, "device")))
	}
	return strings.Join(v, ","), strings.Join(p, ",")
}

// parseLspciMM reads `lspci -mm` output and returns the vendor and device
// names of each VGA/3D/display controller, in file order. The -mm format
// quotes each field, which is why this parses rather than splits on spaces.
func parseLspciMM(out string) (vendors, products []string) {
	for _, line := range strings.Split(out, "\n") {
		// splitQuoted returns only the quoted fields, and the leading PCI
		// slot ("00:02.0") is unquoted -- so the first field here is the
		// class, not the slot: class, vendor, device, ...
		fields := splitQuoted(line)
		if len(fields) < 3 {
			continue
		}
		class := strings.ToLower(fields[0])
		if !strings.Contains(class, "vga") && !strings.Contains(class, "3d controller") &&
			!strings.Contains(class, "display controller") {
			continue
		}
		vendors = append(vendors, fields[1])
		products = append(products, fields[2])
	}
	return vendors, products
}

// splitQuoted splits an lspci -mm line into its quoted fields.
func splitQuoted(line string) []string {
	var out []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range line {
		switch {
		case r == '"':
			if inQuotes {
				out = append(out, cur.String())
				cur.Reset()
			}
			inQuotes = !inQuotes
		case inQuotes:
			cur.WriteRune(r)
		}
	}
	return out
}
