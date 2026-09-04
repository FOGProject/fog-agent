package network

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
)

// ifnet builds one interface address the way net.Interfaces reports it.
func ifnet(cidr string) net.Addr {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return &net.IPNet{IP: ip, Mask: n.Mask}
}

func TestInterfaceForComputesTheLink(t *testing.T) {
	for _, tc := range []struct {
		cidr, network, bcast string
		prefix               int
	}{
		{"10.255.20.7/24", "10.255.20.0", "10.255.20.255", 24},
		{"192.168.1.50/24", "192.168.1.0", "192.168.1.255", 24},
		{"172.16.4.9/16", "172.16.0.0", "172.16.255.255", 16},
		{"192.168.1.66/26", "192.168.1.64", "192.168.1.127", 26},
		{"10.0.0.5/8", "10.0.0.0", "10.255.255.255", 8},
	} {
		got, ok := interfaceFor(ifnet(tc.cidr))
		if !ok {
			t.Errorf("%s: not read", tc.cidr)
			continue
		}
		if got.Network != tc.network {
			t.Errorf("%s: want network %s, got %s", tc.cidr, tc.network, got.Network)
		}
		if got.Broadcast != tc.bcast {
			t.Errorf("%s: want broadcast %s, got %s", tc.cidr, tc.bcast, got.Broadcast)
		}
		if got.Prefix != tc.prefix {
			t.Errorf("%s: want prefix %d, got %d", tc.cidr, tc.prefix, got.Prefix)
		}
	}
}

// The network address is the whole reason this fact exists: it is what the
// server groups on to find a link's other members.
func TestTwoHostsOnOneLinkComputeTheSameNetwork(t *testing.T) {
	a, _ := interfaceFor(ifnet("10.255.20.7/24"))
	b, _ := interfaceFor(ifnet("10.255.20.199/24"))

	if a.Network != b.Network || a.Prefix != b.Prefix {
		t.Fatalf("same link must group: %s/%d vs %s/%d",
			a.Network, a.Prefix, b.Network, b.Prefix)
	}
	// And a machine one subnet over must not.
	c, _ := interfaceFor(ifnet("10.255.21.7/24"))
	if c.Network == a.Network {
		t.Fatalf("different links grouped together on %s", c.Network)
	}
	// Same address, different prefix, is NOT the same link -- which is
	// why the prefix is stored and compared alongside the network.
	d, _ := interfaceFor(ifnet("10.255.20.7/16"))
	if d.Network == a.Network && d.Prefix == a.Prefix {
		t.Fatal("a /16 and a /24 are not the same link")
	}
}

func TestALinkWithNoBroadcastReportsNone(t *testing.T) {
	// A /31 is a point-to-point pair (RFC 3021) and a /32 is a host
	// route. Naming an all-ones address on either would name the peer.
	for _, cidr := range []string{"10.0.0.1/31", "10.0.0.1/32"} {
		got, ok := interfaceFor(ifnet(cidr))
		if !ok {
			t.Errorf("%s: the address is still worth recording", cidr)
			continue
		}
		if got.Broadcast != "" {
			t.Errorf("%s has no broadcast, got %s", cidr, got.Broadcast)
		}
	}
}

func TestIPv6IsNotReported(t *testing.T) {
	// Wake-on-LAN is not defined over v6, and a v6 row would be a column
	// nothing reads.
	//
	// The /120 is the one that needs the To4 guard rather than the
	// prefix range: a v6 mask reports 128 bits, and the 4-in-6
	// correction turns /120 into a plausible-looking /24 with no
	// address behind it.
	for _, cidr := range []string{"fe80::1/64", "fe80::1/120", "2001:db8::1/112"} {
		if row, ok := interfaceFor(ifnet(cidr)); ok {
			t.Errorf("%s was reported as %s/%d", cidr, row.IPv4, row.Prefix)
		}
	}
}

// A non-contiguous mask has no prefix to record. Mask.Size() reports
// (0, 0) for one, which is indistinguishable from a /0 unless it is
// checked -- and a /0 would put the host on "0.0.0.0/0", a link every
// host in the estate would appear to share.
func TestANonContiguousMaskIsNotReported(t *testing.T) {
	addr := &net.IPNet{
		IP:   net.ParseIP("10.255.20.7"),
		Mask: net.IPMask{0xFF, 0x00, 0xFF, 0x00},
	}
	if row, ok := interfaceFor(addr); ok {
		t.Fatalf("reported as %s/%d on network %s",
			row.IPv4, row.Prefix, row.Network)
	}
}

func TestAFourInSixPrefixIsReadAsAFourPrefix(t *testing.T) {
	got, ok := interfaceFor(&net.IPNet{
		IP:   net.ParseIP("10.255.20.7"),
		Mask: net.CIDRMask(96+24, 128),
	})
	if !ok {
		t.Fatal("not read")
	}
	if got.Prefix != 24 || got.Network != "10.255.20.0" {
		t.Fatalf("want 10.255.20.0/24, got %s/%d", got.Network, got.Prefix)
	}
}

func TestANonPrefixAddressIsNotReported(t *testing.T) {
	if _, ok := interfaceFor(&net.UDPAddr{IP: net.ParseIP("10.0.0.1")}); ok {
		t.Fatal("an address with no prefix was reported")
	}
}

// A fake interface set, so the rule can be tested against links this
// machine does not have.
func fakeIfaces() []net.Interface {
	return []net.Interface{
		{
			Name:         "lo",
			Flags:        net.FlagUp | net.FlagRunning | net.FlagLoopback,
			HardwareAddr: net.HardwareAddr{},
		},
		{
			Name:         "eno1",
			Flags:        net.FlagUp | net.FlagRunning | net.FlagBroadcast,
			HardwareAddr: net.HardwareAddr{0x00, 0x11, 0x22, 0x33, 0x44, 0x55},
		},
		{
			Name:         "wlp3s0",
			Flags:        net.FlagUp | net.FlagRunning | net.FlagBroadcast,
			HardwareAddr: net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF},
		},
		{
			// Administratively up, no carrier: `ip link set eno2 up`
			// with the cable out. This is the shape that matters --
			// FlagUp alone is not "this machine can send here".
			Name:         "eno2",
			Flags:        net.FlagUp | net.FlagBroadcast,
			HardwareAddr: net.HardwareAddr{0x00, 0x11, 0x22, 0x33, 0x44, 0x66},
		},
	}
}

// withFakeAddrs points the address lookup at a table, so the rule can be
// driven against links this machine does not have. Without this every
// assertion below is vacuous: a hand-built net.Interface has index 0 and
// the kernel returns nothing for it.
func withFakeAddrs(t *testing.T, table map[string][]string) {
	t.Helper()
	real := addrsOf
	t.Cleanup(func() { addrsOf = real })
	addrsOf = func(iface net.Interface) ([]net.Addr, error) {
		var out []net.Addr
		for _, cidr := range table[iface.Name] {
			out = append(out, ifnet(cidr))
		}
		return out, nil
	}
}

// fakeAddrs is one address per interface in fakeIfaces.
func fakeAddrs() map[string][]string {
	return map[string][]string{
		"lo":     {"127.0.0.1/8"},
		"eno1":   {"10.255.20.7/24"},
		"wlp3s0": {"192.168.1.66/24"},
		"eno2":   {"10.255.30.4/24"},
	}
}

func TestTheLoopbackIsNeverReported(t *testing.T) {
	withFakeAddrs(t, fakeAddrs())
	got := interfacesOf(fakeIfaces())
	if len(got) == 0 {
		t.Fatal("nothing was read, so this asserts nothing")
	}

	for _, row := range got {
		if row.Name == "lo" {
			t.Fatal("the loopback was reported; every host in the estate " +
				"would land on one fake 127.0.0.0/8 link")
		}
		if strings.HasPrefix(row.IPv4, "127.") {
			t.Fatalf("a loopback address was reported: %s", row.IPv4)
		}
	}
}

func TestGatherReadsThisMachine(t *testing.T) {
	n, ok := Gather()
	if !ok {
		t.Fatal("this machine's interfaces could not be read")
	}
	if len(n.Interfaces) == 0 {
		t.Skip("no non-loopback IPv4 interface on this machine")
	}
	for _, row := range n.Interfaces {
		if row.Name == "" {
			t.Error("an interface with no name was reported")
		}
		if net.ParseIP(row.IPv4) == nil {
			t.Errorf("%s: %q is not an address", row.Name, row.IPv4)
		}
		if row.Prefix < 0 || row.Prefix > 32 {
			t.Errorf("%s: prefix %d is out of range", row.Name, row.Prefix)
		}
		if row.Network == "" {
			t.Errorf("%s: no network address, so the server cannot group it",
				row.Name)
		}
		if row.MAC != "" {
			if _, err := net.ParseMAC(row.MAC); err != nil {
				t.Errorf("%s: %q is not a hardware address: %v",
					row.Name, row.MAC, err)
			}
		}
	}
}

// The hash is the resend gate and net.Interfaces promises no order, so
// interfacesOf has to impose one -- otherwise a machine re-reports its
// unchanged interfaces whenever the kernel hands them back differently,
// which is a write per host per poll across the estate forever.
func TestInterfacesOfReturnsAStableOrder(t *testing.T) {
	withFakeAddrs(t, fakeAddrs())

	ifaces := fakeIfaces()
	forward := Network{Interfaces: interfacesOf(ifaces)}

	reversed := make([]net.Interface, len(ifaces))
	for i := range ifaces {
		reversed[len(ifaces)-1-i] = ifaces[i]
	}
	backward := Network{Interfaces: interfacesOf(reversed)}

	if forward.Hash() != backward.Hash() {
		t.Fatalf("the kernel's order reached the hash: %s vs %s",
			forward.Hash(), backward.Hash())
	}
}

// The hash is the resend gate.
func TestTheHashDoesNotMoveWhenNothingChanged(t *testing.T) {
	rows := []Interface{
		{Name: "eno1", IPv4: "10.0.0.2", Network: "10.0.0.0", Prefix: 24},
		{Name: "eno1", IPv4: "10.0.0.1", Network: "10.0.0.0", Prefix: 24},
		{Name: "eno0", IPv4: "192.168.1.5", Network: "192.168.1.0", Prefix: 24},
	}
	shuffled := []Interface{rows[2], rows[0], rows[1]}
	sortInterfaces(rows)
	sortInterfaces(shuffled)

	a := Network{Interfaces: rows}.Hash()
	b := Network{Interfaces: shuffled}.Hash()
	if a != b {
		t.Fatalf("the same interfaces in a different order hashed %s vs %s", a, b)
	}
}

func TestTheHashMovesWhenALinkChanges(t *testing.T) {
	before := Network{Interfaces: []Interface{
		{Name: "eno1", IPv4: "10.0.0.2", Network: "10.0.0.0", Prefix: 24},
	}}
	after := Network{Interfaces: []Interface{
		{Name: "eno1", IPv4: "10.9.0.2", Network: "10.9.0.0", Prefix: 24},
	}}

	if before.Hash() == after.Hash() {
		t.Fatal("a machine that moved subnet must re-report")
	}
}

func TestAnUnpluggedInterfaceIsReportedButNotUp(t *testing.T) {
	// Recorded rather than dropped: the address is still what the host
	// is configured for, and "configured but down" is the state a server
	// picking a relay has to be able to see. Dropping it would look
	// identical to the interface not existing.
	withFakeAddrs(t, fakeAddrs())
	rows := interfacesOf(fakeIfaces())

	var seen bool
	for _, row := range rows {
		if row.Name != "eno2" {
			continue
		}
		seen = true
		if row.Up {
			t.Fatal("an interface with no RUNNING flag was reported up")
		}
	}
	if !seen {
		t.Fatal("the unplugged interface was dropped; it is indistinguishable " +
			"from one that does not exist")
	}
}

// The rows carry the interface's own identity, not just its address.
func TestEachRowCarriesItsInterfacesNameMACAndKind(t *testing.T) {
	withFakeAddrs(t, fakeAddrs())
	rows := interfacesOf(fakeIfaces())

	byName := map[string]Interface{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	wired, ok := byName["eno1"]
	if !ok {
		t.Fatal("eno1 was not reported")
	}
	if wired.MAC != "00:11:22:33:44:55" {
		t.Errorf("want the interface's own MAC, got %q", wired.MAC)
	}
	if !wired.Up {
		t.Error("an up and running interface was reported down")
	}
	if wired.Wireless {
		t.Error("eno1 read as wireless")
	}
	if wired.Network != "10.255.20.0" || wired.Prefix != 24 {
		t.Errorf("want 10.255.20.0/24, got %s/%d", wired.Network, wired.Prefix)
	}
	if wifi := byName["wlp3s0"]; !wifi.Wireless {
		t.Error("wlp3s0 did not read as wireless")
	}
}

// One interface with two addresses is on two links and can broadcast on
// both, so it is two rows.
func TestAnInterfaceWithTwoAddressesIsTwoLinks(t *testing.T) {
	table := fakeAddrs()
	table["eno1"] = []string{"10.255.20.7/24", "10.255.99.7/24"}
	withFakeAddrs(t, table)

	var links []string
	for _, row := range interfacesOf(fakeIfaces()) {
		if row.Name == "eno1" {
			links = append(links, row.Network)
		}
	}
	if len(links) != 2 {
		t.Fatalf("want two rows for eno1, got %v", links)
	}
}

// One unreadable interface must not cost the others their rows.
func TestOneUnreadableInterfaceDoesNotSinkTheRest(t *testing.T) {
	real := addrsOf
	t.Cleanup(func() { addrsOf = real })
	table := fakeAddrs()
	addrsOf = func(iface net.Interface) ([]net.Addr, error) {
		if iface.Name == "eno1" {
			return nil, errors.New("permission denied")
		}
		var out []net.Addr
		for _, cidr := range table[iface.Name] {
			out = append(out, ifnet(cidr))
		}
		return out, nil
	}

	rows := interfacesOf(fakeIfaces())
	if len(rows) == 0 {
		t.Fatal("one bad interface silenced them all")
	}
	for _, row := range rows {
		if row.Name == "eno1" {
			t.Fatal("the unreadable interface produced a row anyway")
		}
	}
}

func TestWirelessIsGuessedFromTheName(t *testing.T) {
	for _, name := range []string{
		"wlan0", "wlp3s0", "wlx001122334455", "Wi-Fi", "WiFi",
		"Wireless Network Connection", "en0",
	} {
		if !looksWireless(name) {
			t.Errorf("%q should read as wireless", name)
		}
	}
	for _, name := range []string{
		"eno1", "eth0", "enp0s31f6", "Ethernet", "en01", "br0", "docker0",
	} {
		if looksWireless(name) {
			t.Errorf("%q should not read as wireless", name)
		}
	}
}

// Every field on the wire, always, so the server writes a whole row rather
// than merging against a stale one (design 0006).
func TestEveryFieldIsAlwaysSent(t *testing.T) {
	b, err := json.Marshal(Interface{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"name", "mac", "ipv4", "prefix", "network", "broadcast", "up", "wireless",
	} {
		if !strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("%q is omitted from an empty row: %s", key, b)
		}
	}
}
