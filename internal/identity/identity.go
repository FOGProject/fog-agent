// Package identity answers "which host record is this?" with the same
// evidence fogproject uses at PXE boot: the SMBIOS identity tuple plus the
// MAC list. It never authenticates; that is the enrollment certificate's job
// (design doc section 4).
package identity

import (
	"net"
	"sort"

	"github.com/FOGProject/fog-agent/internal/identity/smbios"
)

// Host is everything the agent sends the server to be resolved.
type Host struct {
	smbios.Identity
	// SMBIOSVersion is the version the entry point declared. It decides the
	// UUID byte order, so when two readers disagree about a UUID this is
	// the first thing to compare.
	SMBIOSVersion string   `json:"smbios_version,omitempty"`
	MACs          []string `json:"macs"`
	// Warnings records fields that could not be read and why, so an empty
	// value is distinguishable from an unreadable one in the check-in.
	Warnings []string `json:"warnings,omitempty"`
}

// Read collects the SMBIOS tuple via the OS-specific reader and the MACs of
// every non-loopback interface that has a hardware address.
func Read() Host {
	h := readSMBIOS()
	h.MACs = macs()
	return h
}

func macs() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 || len(i.HardwareAddr) == 0 {
			continue
		}
		out = append(out, i.HardwareAddr.String())
	}
	sort.Strings(out)
	return out
}
