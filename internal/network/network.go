// Package network reports the machine's own network interfaces: which
// links it is on, and what each link's broadcast address is (design 0011
// §3).
//
// This is the fact that makes the wake relay possible. FOG has never
// recorded a host's interfaces at all -- `hosts.hostIP` is whatever the
// host last resolved to, with no prefix and no notion of which of several
// interfaces it came from -- so "which machines share a link with host 41"
// has not been a question the server could answer. Two hosts whose
// addresses fall in the same network with the same prefix are on the same
// link, and that is one row each.
//
// It earns its place beyond the relay. A host's interfaces are inventory in
// every other tool that does inventory, and once they are recorded, "what
// is on 10.20.30.0/24", "which host holds this MAC" and "this machine has
// been on three subnets this month" are all reports rather than
// investigations.
//
// Unlike every other fact in this agent there is NO per-platform file.
// net.Interfaces() is one of the few places the Go runtime already does the
// platform work -- GetAdaptersAddresses on Windows, netlink on Linux,
// getifaddrs on the BSDs -- and it returns the same shape everywhere. A
// gather_windows.go here would be a WMI query re-deriving what the runtime
// already had.
package network

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Interface is one of the machine's links.
//
// Every field is always sent (no omitempty), for design 0006's reason: the
// block is the whole row, so an unknown field is an explicit empty value
// and the server writes a full row rather than merging against a stale one.
type Interface struct {
	// Name is the OS's name for it: eno1, Ethernet, wlp3s0.
	Name string `json:"name"`
	// MAC is the interface's own hardware address, lowercased with colons.
	// This is the address a magic packet for THIS host would carry, which
	// is what makes the table answer "who holds this MAC" as well.
	MAC string `json:"mac"`
	// IPv4 is the address on this link. One row per address: an interface
	// with two addresses is on two links and can broadcast on both.
	IPv4 string `json:"ipv4"`
	// Prefix is the network prefix length, 0-32.
	Prefix int `json:"prefix"`
	// Network is the network address -- IPv4 masked to Prefix. Sent rather
	// than left for the server to compute, because it is what the server
	// GROUPs and JOINs on to find a link's other members, and an
	// expression like INET_ATON(ip) & mask cannot use an index.
	Network string `json:"network"`
	// Broadcast is the link's broadcast address, empty where the link has
	// none (a /31 point-to-point pair, a /32 host route, a tunnel).
	Broadcast string `json:"broadcast"`
	// Up is whether the interface is up AND running. A configured but
	// unplugged NIC is a link this machine cannot send on, and asking it
	// to relay a wake there is asking for a silent failure.
	Up bool `json:"up"`
	// Wireless is a best-effort guess from the interface name. Recorded
	// because a wireless link is a poor relay -- the neighboring machine
	// is asleep, and its AP will not bridge a broadcast to a station that
	// is not associated -- and a server picking senders can prefer wired.
	Wireless bool `json:"wireless"`
}

// Network is what the machine says about its own links.
type Network struct {
	Interfaces []Interface `json:"interfaces"`
}

// Gather collects the machine's interfaces. The bool is "a collector ran
// here", never "the machine has interfaces": a machine whose interfaces
// could not be read returns false and the block is omitted from the poll
// entirely, because the server treats a reported list as complete and an
// empty one would say every link this host was on had gone away.
func Gather() (Network, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return Network{}, false
	}

	return Network{Interfaces: interfacesOf(ifaces)}, true
}

// addrsOf reads one interface's addresses. A variable rather than a direct
// call because net.Interface.Addrs() goes to the kernel and matches on the
// interface INDEX, so a hand-built net.Interface returns nothing -- which
// would make every test of the rule below quietly vacuous rather than
// wrong.
var addrsOf = func(iface net.Interface) ([]net.Addr, error) {
	return iface.Addrs()
}

// interfacesOf is the rule, split from the syscall so it can be tested
// against interfaces this machine does not have.
func interfacesOf(ifaces []net.Interface) []Interface {
	var out []Interface
	for _, iface := range ifaces {
		// The loopback is on no link anyone else shares, and it is on
		// every machine, so recording it would put every host in the
		// estate in one enormous fake "127.0.0.0/8 link".
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := addrsOf(iface)
		if err != nil {
			// One unreadable interface is not a reason to report none
			// of the others.
			continue
		}
		// String() is already lowercase colon-separated hex, which is the
		// form the server compares against hostMAC.
		mac := iface.HardwareAddr.String()
		up := iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagRunning != 0
		wireless := looksWireless(iface.Name)

		for _, addr := range addrs {
			row, ok := interfaceFor(addr)
			if !ok {
				continue
			}
			row.Name = iface.Name
			row.MAC = mac
			row.Up = up
			row.Wireless = wireless
			out = append(out, row)
		}
	}
	sortInterfaces(out)

	return out
}

// interfaceFor is the addressing half: what link this address is on.
func interfaceFor(addr net.Addr) (Interface, bool) {
	ipnet, ok := addr.(*net.IPNet)
	if !ok {
		return Interface{}, false
	}
	ip := ipnet.IP.To4()
	if ip == nil {
		// IPv6 is not reported. Wake-on-LAN is not defined over it, and
		// a v6 row would be a column of addresses nothing reads --
		// design 0006's rule that a fact has to have a consumer.
		return Interface{}, false
	}
	ones, bits := ipnet.Mask.Size()
	if bits == 0 {
		// A non-contiguous mask. Size() reports 0,0 and there is no
		// prefix to record.
		return Interface{}, false
	}
	if bits == 128 {
		// A v4 address carrying a v6-shaped mask, which is how a
		// 4-in-6 address comes back from some stacks.
		ones -= 96
	}
	if ones < 0 || ones > 32 {
		return Interface{}, false
	}
	mask := net.CIDRMask(ones, 32)

	network := ip.Mask(mask)
	row := Interface{
		IPv4:    ip.String(),
		Prefix:  ones,
		Network: network.String(),
	}
	// A /31 is a point-to-point pair (RFC 3021) and a /32 is a host
	// route; neither has a broadcast address, and saying otherwise would
	// name the peer.
	if ones < 31 {
		bcast := make(net.IP, net.IPv4len)
		for i := range bcast {
			bcast[i] = ip[i] | ^mask[i]
		}
		row.Broadcast = bcast.String()
	}

	return row, true
}

// wirelessPrefixes are the interface-name conventions that mean wireless.
// Linux predictable names use wl, the older ones wlan; Windows and macOS
// spell it out. A guess, and named as one -- the alternative is a WMI query
// on one platform and a nl80211 socket on another for a hint.
var wirelessPrefixes = []string{"wlan", "wlp", "wlx", "wl", "wifi", "wi-fi", "en0"}

// looksWireless guesses from the interface name.
func looksWireless(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "wireless") || strings.Contains(lower, "wi-fi") ||
		strings.Contains(lower, "wifi") {
		return true
	}
	for _, p := range wirelessPrefixes {
		if p == "en0" {
			// macOS's wireless interface, and only an exact match:
			// en0 is the wired port on plenty of other Macs, so this
			// is the weakest guess here and deliberately not a
			// prefix match that would also claim en01.
			if lower == "en0" {
				return true
			}
			continue
		}
		if strings.HasPrefix(lower, p) {
			return true
		}
	}

	return false
}

// sortInterfaces puts the rows in a stable order.
//
// Not cosmetic: the hash below is the resend gate, and net.Interfaces does
// not promise an order. Without this a machine would re-report its
// unchanged interfaces whenever the kernel handed them back differently.
func sortInterfaces(in []Interface) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Name != in[j].Name {
			return in[i].Name < in[j].Name
		}
		return in[i].IPv4 < in[j].IPv4
	})
}

// Hash is the resend gate, same construction as inventory's.
func (n Network) Hash() string {
	b, err := json.Marshal(n)
	if err != nil {
		// A struct of strings, ints and bools does not fail to marshal;
		// if it somehow did, a changing hash forces a resend rather than
		// hiding the fault.
		return fmt.Sprintf("unmarshalable-%v", err)
	}
	sum := sha256.Sum256(b)

	return fmt.Sprintf("%x", sum[:8])
}
