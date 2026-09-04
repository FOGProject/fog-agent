// Package wake sends Wake-on-LAN magic packets for OTHER FOG hosts on this
// machine's own links (design 0011).
//
// It exists because a magic packet is a link-layer broadcast and FOG can only
// send one from a machine it owns. In a routed estate a subnet often has FOG
// hosts on it and no FOG server or storage node, and the documented
// workaround -- directed broadcast -- has been off by default on enterprise
// routers since the smurf attack made it dangerous. An agent that is already
// awake on that link is the sender that was always there.
//
// The security shape is the whole design, so it is worth stating in the code
// rather than only in the document:
//
//   - THERE IS NO DESTINATION ON THE WIRE. The agent sends to its own
//     interfaces' broadcast addresses and to 255.255.255.255, and there is no
//     field in which a caller could name anywhere else. An agent that could be
//     aimed is a UDP reflector, and the fact that only the server can aim it
//     today is not a property worth relying on.
//   - The MAC is parsed and re-serialized here, never passed through. A
//     malformed one becomes a refusal, not a datagram.
//   - The payload is always exactly the 102-byte magic packet. Nothing else
//     is ever put on the wire.
//   - The work per poll is bounded by MaxTargets, a constant here rather than
//     a number the server supplies, for the same reason the destination is not
//     on the wire.
package wake

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Port is where a magic packet goes. 9 (discard) is what FOG's own
// WakeOnLan::send uses and what every NIC firmware listens for; 7 (echo) is
// the other conventional choice and adds nothing.
const Port = 9

// MaxTargets bounds one poll's work. Deliberately a constant and not a
// server-sent number: an agent whose traffic ceiling is set by whatever
// answers its poll has no ceiling.
const MaxTargets = 32

// Timeout bounds the whole send. A socket that will not write must not hold
// the poll open.
const Timeout = 10 * time.Second

// What the agent reports for one target. These are the server's
// WakeRelay::STATUSES.
const (
	// StatusSent is at least one packet on the wire.
	StatusSent = "sent"
	// StatusFailed is a target nothing could be sent for.
	StatusFailed = "failed"
)

// Target is one FOG host to wake, as the server named it.
//
// There is no address here and there must never be one. The MACs are the
// host's own rows on the server; the agent chooses where to broadcast.
type Target struct {
	// ID is the FOG host id, so the server can record what happened
	// against the right row.
	ID int `json:"id"`
	// MACs are that host's hardware addresses, in any of the forms FOG
	// stores. Re-parsed here.
	MACs []string `json:"macs"`
}

// Policy is the wake block of the desired state: hosts on this machine's
// links that the server wants woken.
type Policy struct {
	Targets []Target `json:"targets"`
}

// Report is what happened for one target.
type Report struct {
	Target Target
	Status string
	// Packets is how many datagrams went out: one per MAC per broadcast
	// address. It is the useful number, because "sent" with a count of
	// zero would be a lie and FOG's existing wake path cannot tell the
	// difference at all.
	Packets int
	Error   string
	Detail  string
}

// Sender puts a datagram on a broadcast address. An interface so the tests
// can watch exactly what would have gone on the wire without a network.
type Sender interface {
	// Broadcasts is where a packet should go: every local broadcast
	// address, plus the limited broadcast.
	Broadcasts() ([]net.IP, error)
	// Send writes one datagram to one address.
	Send(dst net.IP, payload []byte) error
}

// Run sends for every target and reports each one.
//
// A target with no usable MAC is a failure and not a silent skip: the server
// asked for a wake, and "nothing happened and nobody said so" is the
// behavior of the path this replaces.
func Run(sender Sender, policy Policy) []Report {
	if len(policy.Targets) == 0 {
		return nil
	}

	targets := policy.Targets
	if len(targets) > MaxTargets {
		targets = targets[:MaxTargets]
	}

	dsts, err := sender.Broadcasts()
	if err != nil || len(dsts) == 0 {
		// No link to broadcast on. Reported per target rather than
		// swallowed, because from the server's side this is
		// indistinguishable from a machine that sent and was ignored.
		detail := "no broadcast address on any interface"
		if err != nil {
			detail = "could not read this machine's interfaces: " + err.Error()
		}
		reports := make([]Report, 0, len(targets))
		for _, t := range targets {
			reports = append(reports, Report{Target: t,
				Status: StatusFailed, Error: detail, Detail: detail})
		}
		return reports
	}

	reports := make([]Report, 0, len(targets))
	for _, t := range targets {
		reports = append(reports, wakeOne(sender, t, dsts))
	}
	return reports
}

// wakeOne sends one host's packets.
func wakeOne(sender Sender, t Target, dsts []net.IP) Report {
	macs, bad := parseMACs(t.MACs)
	if len(macs) == 0 {
		why := "no usable MAC address"
		if bad != "" {
			why += ": " + bad
		}
		return Report{Target: t, Status: StatusFailed, Error: why, Detail: why}
	}

	var sent int
	var lastErr error
	for _, mac := range macs {
		packet := magicPacket(mac)
		for _, dst := range dsts {
			if err := sender.Send(dst, packet); err != nil {
				lastErr = err
				continue
			}
			sent++
		}
	}
	if sent == 0 {
		why := "no packet could be sent"
		if lastErr != nil {
			why = lastErr.Error()
		}
		return Report{Target: t, Status: StatusFailed, Error: why, Detail: why}
	}

	return Report{
		Target: t, Status: StatusSent, Packets: sent,
		Detail: fmt.Sprintf("%d packet(s) for %d MAC(s) on %d broadcast address(es)",
			sent, len(macs), len(dsts)),
	}
}

// magicPacket is the 102 bytes: six 0xFF, then the MAC sixteen times.
//
// Built here from a parsed address rather than from the string the server
// sent, which is the difference between putting a known 102 bytes on the
// wire and putting whatever arrived on it.
func magicPacket(mac net.HardwareAddr) []byte {
	packet := make([]byte, 0, 6+16*len(mac))
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, mac...)
	}
	return packet
}

// parseMACs turns the server's strings into addresses, dropping duplicates
// and anything that is not a 6-byte MAC. The second return names the first
// rejected value, for the report.
//
// Every form FOG stores has to read: `MACAddress::PATTERN` accepts colons,
// hyphens, twelve bare hex digits and dot-separated quads, so a row written
// by any of FOG's twenty years of entry points has to survive the trip.
func parseMACs(in []string) ([]net.HardwareAddr, string) {
	seen := make(map[string]bool, len(in))
	var out []net.HardwareAddr
	var bad string
	for _, raw := range in {
		mac, err := parseMAC(raw)
		if err != nil {
			if bad == "" {
				bad = strings.TrimSpace(raw)
			}
			continue
		}
		key := mac.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, mac)
	}
	return out, bad
}

// errNotAMAC is a value that is not a 6-byte hardware address.
var errNotAMAC = errors.New("not a 6-byte MAC address")

// parseMAC accepts FOG's forms and rejects everything else.
//
// net.ParseMAC covers all four spellings MACAddress::PATTERN allows --
// colons, hyphens, dot-separated quads and twelve bare hex digits -- so
// there is nothing to add for the forms. What it does need is the length
// check: ParseMAC also accepts 8- and 20-byte addresses (EUI-64,
// InfiniBand), and neither is what a magic packet carries.
func parseMAC(raw string) (net.HardwareAddr, error) {
	mac, err := net.ParseMAC(strings.TrimSpace(raw))
	if err != nil || len(mac) != 6 {
		return nil, errNotAMAC
	}

	return mac, nil
}
