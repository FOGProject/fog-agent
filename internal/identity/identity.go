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
// the machine's hardware network interfaces.
func Read() Host {
	h := readSMBIOS()
	ifs, err := net.Interfaces()
	if err == nil {
		h.MACs = pickMACs(ifs, hardwareFilter())
	}
	return h
}

// pickMACs returns the sorted MACs of every non-loopback interface that has a
// hardware address and that isHardware accepts.
//
// Why the filter: a software adapter (a VPN TAP, a tunnel) keeps its MAC when
// a disk is imaged, so every clone of a golden image reports it. The server
// then finds two hosts for one MAC and answers "conflict", and the clone never
// rejoins the directory. A hardware NIC's MAC belongs to that machine alone.
//
// A nil isHardware means the OS could not say which interfaces are hardware.
// The agent then sends every MAC, as it did before the filter. So does a
// machine where the filter accepts no interface: the full MAC list is safer
// than an empty one, which leaves SMBIOS as the only evidence.
func pickMACs(ifs []net.Interface, isHardware func(net.Interface) bool) []string {
	var all, hw []string
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 || len(i.HardwareAddr) == 0 {
			continue
		}
		mac := i.HardwareAddr.String()
		all = append(all, mac)
		if isHardware != nil && isHardware(i) {
			hw = append(hw, mac)
		}
	}
	out := hw
	if len(out) == 0 {
		out = all
	}
	sort.Strings(out)
	return out
}
