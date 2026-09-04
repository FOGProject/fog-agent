package wake

import (
	"context"
	"net"
	"testing"
	"time"
)

// ifnet builds one interface address the way net.Interfaces reports it.
func ifnet(cidr string) net.Addr {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return &net.IPNet{IP: ip, Mask: n.Mask}
}

func TestBroadcastForComputesTheLinkBroadcast(t *testing.T) {
	for _, tc := range []struct{ cidr, want string }{
		{"10.255.20.7/24", "10.255.20.255"},
		{"192.168.1.50/24", "192.168.1.255"},
		{"172.16.4.9/16", "172.16.255.255"},
		{"10.0.0.5/8", "10.255.255.255"},
		{"192.168.1.66/26", "192.168.1.127"},
		{"169.254.3.4/16", "169.254.255.255"},
	} {
		got := broadcastFor(ifnet(tc.cidr))
		if got == nil || got.String() != tc.want {
			t.Errorf("%s: want %s, got %v", tc.cidr, tc.want, got)
		}
	}
}

func TestBroadcastForRefusesWhatHasNoBroadcast(t *testing.T) {
	// A /31 is a point-to-point pair (RFC 3021) and a /32 is a host
	// route. Sending the all-ones address on either is sending to the
	// peer, or to nothing.
	for _, cidr := range []string{"10.0.0.1/31", "10.0.0.1/32"} {
		if got := broadcastFor(ifnet(cidr)); got != nil {
			t.Errorf("%s has no broadcast, got %v", cidr, got)
		}
	}
	// Wake-on-LAN is not defined over IPv6, which has no broadcast at
	// all -- multicast replaced it.
	if got := broadcastFor(ifnet("fe80::1/64")); got != nil {
		t.Errorf("IPv6 has no broadcast, got %v", got)
	}
	// Not an IPNet at all.
	if got := broadcastFor(&net.UDPAddr{IP: net.ParseIP("10.0.0.1")}); got != nil {
		t.Errorf("a non-prefix address has no broadcast, got %v", got)
	}
}

// broadcastFor must read a v4 prefix stored in v6 form, which is how a
// 4-in-6 address comes back from some stacks.
func TestBroadcastForReadsAFourInSixPrefix(t *testing.T) {
	addr := &net.IPNet{
		IP:   net.ParseIP("10.255.20.7"),
		Mask: net.CIDRMask(96+24, 128),
	}
	got := broadcastFor(addr)
	if got == nil || got.String() != "10.255.20.255" {
		t.Fatalf("want 10.255.20.255, got %v", got)
	}
}

func TestBroadcastsOfSkipsLinksThatCannotCarryOne(t *testing.T) {
	ifaces := []net.Interface{
		{Name: "lo", Flags: net.FlagUp | net.FlagLoopback},
		{Name: "down0", Flags: net.FlagBroadcast},
		{Name: "tun0", Flags: net.FlagUp | net.FlagPointToPoint},
	}
	got := broadcastsOf(ifaces)

	// The limited broadcast is the floor: it is the one address that
	// still works when nothing else can be computed.
	if len(got) != 1 || got[0].String() != "255.255.255.255" {
		t.Fatalf("want only the limited broadcast, got %v", got)
	}
}

func TestBroadcastsOfAlwaysLeadsWithTheLimitedBroadcast(t *testing.T) {
	got := broadcastsOf(nil)
	if len(got) != 1 || !got[0].Equal(net.IPv4bcast) {
		t.Fatalf("want 255.255.255.255, got %v", got)
	}
}

// The real socket, writing the real bytes. This does NOT prove
// SO_BROADCAST is set -- removing the setsockopt was tried and this stayed
// green, because a current Linux kernel does not enforce it. The option is
// gated by TestTheSocketReallyHasBroadcastPermission instead.
func TestUDPSenderReallyWritesToABroadcastAddress(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	defer listener.Close()
	port := listener.LocalAddr().(*net.UDPAddr).Port

	sender, err := NewUDPSender(context.Background())
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	defer sender.Close()
	// The loopback net's broadcast, so the packet never leaves this
	// machine. Nothing else about the socket differs from a real send.
	sender.port = port

	want := magicPacket(net.HardwareAddr{0x00, 0x11, 0x22, 0x33, 0x44, 0x55})
	if err := sender.Send(net.ParseIP("127.255.255.255"), want); err != nil {
		t.Fatalf("send: %v", err)
	}

	if err := listener.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	buf := make([]byte, 512)
	n, _, err := listener.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != len(want) || string(buf[:n]) != string(want) {
		t.Fatalf("the bytes on the wire are not the magic packet (%d bytes)", n)
	}
}

func TestSendOnAClosedSenderIsAnError(t *testing.T) {
	sender, err := NewUDPSender(context.Background())
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	if err := sender.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := sender.Send(net.IPv4bcast, []byte("x")); err == nil {
		t.Fatal("a closed sender must refuse rather than pretend")
	}
	// Closing twice is what a deferred Close after an explicit one does.
	if err := sender.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// Broadcasts reads the interfaces this machine actually has. Asserted
// loosely on purpose: the machine running the suite is not a fixture.
func TestBroadcastsReadsThisMachine(t *testing.T) {
	sender, err := NewUDPSender(context.Background())
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	defer sender.Close()

	dsts, err := sender.Broadcasts()
	if err != nil {
		t.Fatalf("broadcasts: %v", err)
	}
	if len(dsts) == 0 || !dsts[0].Equal(net.IPv4bcast) {
		t.Fatalf("want the limited broadcast at least, got %v", dsts)
	}
	for _, d := range dsts {
		if d.To4() == nil {
			t.Errorf("a non-v4 destination reached the list: %v", d)
		}
	}
}
