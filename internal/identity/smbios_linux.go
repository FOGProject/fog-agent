package identity

import (
	"fmt"
	"os"
	"strings"
)

// readSMBIOS reads the kernel's decoded DMI fields. product_serial,
// board_serial and chassis_asset_tag are root-only (0400); the agent runs as
// root, but a non-root `fog-agent identity` still reports the UUID and says
// which fields it could not read.
func readSMBIOS() Host {
	var h Host
	read := func(name string) string {
		b, err := os.ReadFile("/sys/class/dmi/id/" + name)
		if err != nil {
			h.Warnings = append(h.Warnings, fmt.Sprintf("%s: %v", name, err))
			return ""
		}
		return strings.TrimRight(string(b), "\n")
	}
	h.SystemUUID = read("product_uuid")
	h.SystemSerial = read("product_serial")
	h.BoardSerial = read("board_serial")
	h.ChassisAsset = read("chassis_asset_tag")
	h.SMBIOSVersion = entryPointVersion(&h)
	return h
}

// entryPointVersion reads the raw anchor the kernel used. "_SM3_" carries
// major/minor at bytes 7 and 8, "_SM_" at bytes 6 and 7. Root only.
func entryPointVersion(h *Host) string {
	b, err := os.ReadFile("/sys/firmware/dmi/tables/smbios_entry_point")
	if err != nil {
		h.Warnings = append(h.Warnings, fmt.Sprintf("smbios_entry_point: %v", err))
		return ""
	}
	switch {
	case len(b) >= 9 && string(b[:5]) == "_SM3_":
		return fmt.Sprintf("%d.%d", b[7], b[8])
	case len(b) >= 8 && string(b[:4]) == "_SM_":
		return fmt.Sprintf("%d.%d", b[6], b[7])
	}
	return ""
}
