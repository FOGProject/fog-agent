// Package netboot arms a one-shot network boot in UEFI firmware (design
// 0013). A FOG task runs in FOS, which the machine reaches by booting from
// the network; a task reboot that lands back on the local disk has done
// nothing but take the machine away from whoever was using it.
//
// The mechanism is the EFI global variable BootNext: one UINT16 naming a
// Boot#### option, which the firmware boots once and then deletes by
// itself. Nothing here ever writes BootOrder -- a request for one boot that
// expires on its own is a very different thing from taking ownership of a
// machine-wide firmware setting on a machine that may never boot again.
package netboot

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"
)

// EFIGlobal is the EFI global variable namespace, the GUID every variable
// named here lives under.
const EFIGlobal = "8be4df61-93ca-11d2-aa0d-00e098032b8c"

// The two failures a caller must tell apart. "This machine has no UEFI" and
// "this machine has UEFI but no network boot entry" look the same from the
// outside and mean completely different things to an admin: the first is a
// BIOS box that needs its boot order set, the second is a UEFI box with PXE
// switched off in firmware setup, or firmware that never persists a network
// entry at all.
var (
	// ErrUnsupported is returned when there is no UEFI to talk to: a
	// BIOS/CSM machine, a kernel with no efivars, or macOS.
	ErrUnsupported = errors.New("netboot: firmware variables are not available")
	// ErrNoOption is returned when the firmware answered but holds no
	// network boot entry to point BootNext at.
	ErrNoOption = errors.New("netboot: firmware has no network boot entry")
)

// Option is one firmware boot entry that boots from the network.
type Option struct {
	// Number is the #### in Boot####.
	Number uint16
	// Description is the firmware's own label, for telling an admin which
	// entry was armed. It is never used to decide what an entry is.
	Description string
	// IPv4 is true when the device path names an IPv4 node. FOG serves
	// PXE over IPv4, so an IPv6-only entry would arm a boot that reaches
	// no FOG server.
	IPv4 bool
}

// String renders an option the way it should appear in a log line or a
// result detail.
func (o Option) String() string {
	return fmt.Sprintf("Boot%04X (%s)", o.Number, o.Description)
}

// loadOption is one parsed EFI_LOAD_OPTION.
type loadOption struct {
	active      bool
	network     bool
	ipv4        bool
	description string
}

// LOAD_OPTION_ACTIVE. An entry without it is one the firmware will not
// boot, so it is not a candidate however good its device path looks.
const loadOptionActive = 0x00000001

// Device path node types and subtypes, from the UEFI spec. A network boot
// path always carries a MAC node; the IP nodes say which stack it uses.
const (
	dpTypeMessaging = 0x03
	dpTypeEnd       = 0x7f

	dpMsgMACAddr = 0x0b
	dpMsgIPv4    = 0x0c
	dpMsgIPv6    = 0x0d
)

// parseLoadOption decodes an EFI_LOAD_OPTION:
//
//	UINT32           Attributes
//	UINT16           FilePathListLength
//	CHAR16           Description[]      -- NUL terminated
//	EFI_DEVICE_PATH  FilePathList[]     -- FilePathListLength bytes
//	UINT8            OptionalData[]
//
// A malformed entry is an error rather than a "not a network option": the
// two are different, and silently treating a truncated variable as "no PXE
// here" is how a machine that could have netbooted gets reported as one
// that cannot.
func parseLoadOption(b []byte) (loadOption, error) {
	var lo loadOption
	if len(b) < 6 {
		return lo, fmt.Errorf("load option is %d bytes, too short for a header", len(b))
	}
	attrs := binary.LittleEndian.Uint32(b[0:4])
	fpLen := int(binary.LittleEndian.Uint16(b[4:6]))
	lo.active = attrs&loadOptionActive != 0

	// The description is UTF-16LE up to the first NUL unit. Walking in
	// 2-byte steps and stopping at the terminator is the only way to find
	// where the device path starts.
	rest := b[6:]
	var units []uint16
	end := -1
	for i := 0; i+1 < len(rest); i += 2 {
		u := binary.LittleEndian.Uint16(rest[i : i+2])
		if u == 0 {
			end = i + 2
			break
		}
		units = append(units, u)
	}
	if end < 0 {
		return lo, errors.New("load option description is not terminated")
	}
	lo.description = string(utf16.Decode(units))

	dp := rest[end:]
	if fpLen > len(dp) {
		return lo, fmt.Errorf("device path claims %d bytes, %d present", fpLen, len(dp))
	}
	lo.network, lo.ipv4 = devicePathIsNetwork(dp[:fpLen])
	return lo, nil
}

// devicePathIsNetwork walks the device path nodes looking for the
// messaging nodes that mark a network boot. Each node is
// UINT8 Type, UINT8 SubType, UINT16 Length, where Length counts the header
// too.
//
// This is deliberately not a match on the description. "UEFI PXEv4",
// "Network Boot" and "IBA GE Slot 0100 v1550" are the same thing under
// three names, in however many languages the firmware ships, and the bytes
// already state the fact that the string only hints at.
func devicePathIsNetwork(dp []byte) (network bool, ipv4 bool) {
	for len(dp) >= 4 {
		t := dp[0]
		st := dp[1]
		l := int(binary.LittleEndian.Uint16(dp[2:4]))
		// A node shorter than its own header, or longer than what is
		// left, means the path is corrupt; stop rather than loop
		// forever or read past the end.
		if l < 4 || l > len(dp) {
			return network, ipv4
		}
		if t == dpTypeEnd {
			return network, ipv4
		}
		if t == dpTypeMessaging {
			switch st {
			case dpMsgMACAddr:
				network = true
			case dpMsgIPv4:
				network, ipv4 = true, true
			case dpMsgIPv6:
				network = true
			}
		}
		dp = dp[l:]
	}
	return network, ipv4
}

// choose picks the entry to arm from the firmware's own BootOrder.
//
// BootOrder is the firmware's stated preference and is honoured first; the
// only reordering is that an IPv4 entry beats an IPv6-only one, because FOG
// serves PXE over IPv4 and arming an IPv6 boot would send the machine
// looking for a server that is not there.
func choose(order []uint16, opts map[uint16]loadOption) (Option, bool) {
	var fallback Option
	haveFallback := false
	for _, n := range order {
		lo, ok := opts[n]
		if !ok || !lo.active || !lo.network {
			continue
		}
		o := Option{Number: n, Description: lo.description, IPv4: lo.ipv4}
		if lo.ipv4 {
			return o, true
		}
		if !haveFallback {
			fallback, haveFallback = o, true
		}
	}
	return fallback, haveFallback
}

// The OS-specific halves, replaced in tests.
var (
	readVar   = osReadVar
	writeVar  = osWriteVar
	deleteVar = osDeleteVar
)

// Find returns the network boot entry the agent would arm, or ErrNoOption
// when the firmware holds none. ErrUnsupported means there is no UEFI here
// at all.
func Find() (Option, error) {
	raw, err := readVar("BootOrder")
	if err != nil {
		return Option{}, err
	}
	if len(raw)%2 != 0 {
		return Option{}, fmt.Errorf("netboot: BootOrder is %d bytes, not a UINT16 list", len(raw))
	}
	order := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		order = append(order, binary.LittleEndian.Uint16(raw[i:i+2]))
	}
	opts := make(map[uint16]loadOption, len(order))
	for _, n := range order {
		b, err := readVar(fmt.Sprintf("Boot%04X", n))
		if err != nil {
			// One unreadable entry is not fatal: firmware is free to
			// list an option in BootOrder that it no longer holds, and
			// the other entries are still worth looking at.
			continue
		}
		lo, err := parseLoadOption(b)
		if err != nil {
			continue
		}
		opts[n] = lo
	}
	o, ok := choose(order, opts)
	if !ok {
		return Option{}, ErrNoOption
	}
	return o, nil
}

// Arm sets BootNext to o, so the next boot -- and only the next -- goes to
// the network. The firmware deletes the variable as it consumes it, so
// there is nothing to undo on the far side.
func Arm(o Option) error {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], o.Number)
	return writeVar("BootNext", b[:])
}

// Disarm removes a BootNext this agent set. Only for the case where the
// reboot that the arming was for did not happen: a BootNext left behind
// would send the machine to the network on whatever boot came next, for
// whatever reason.
func Disarm() error {
	return deleteVar("BootNext")
}
