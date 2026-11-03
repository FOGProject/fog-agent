package identity

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hardwareInterfaceFlag is the HardwareInterface bit, the lowest bit of
// MIB_IF_ROW2.InterfaceAndOperStatusFlags (netioapi.h). Windows sets it for
// a NIC that a hardware device backs, and clears it for a VPN TAP, a tunnel,
// or a Hyper-V vEthernet.
const hardwareInterfaceFlag = 0x01

// hardwareFilter accepts an interface whose MAC belongs to a hardware
// interface in the Windows interface table. It matches by MAC because
// net.Interfaces does not expose the flag. It returns nil when the table
// cannot be read, so the caller sends every MAC.
func hardwareFilter() func(net.Interface) bool {
	var table *windows.MibIfTable2
	if windows.GetIfTable2Ex(windows.MibIfTableNormalWithoutStatistics, &table) != nil || table == nil {
		return nil
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	hw := map[string]bool{}
	for _, row := range unsafe.Slice(&table.Table[0], table.NumEntries) {
		n := row.PhysicalAddressLength
		if row.InterfaceAndOperStatusFlags&hardwareInterfaceFlag == 0 || n == 0 || n > uint32(len(row.PhysicalAddress)) {
			continue
		}
		hw[net.HardwareAddr(row.PhysicalAddress[:n]).String()] = true
	}
	return func(i net.Interface) bool { return hw[i.HardwareAddr.String()] }
}
