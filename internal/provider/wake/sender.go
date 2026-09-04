package wake

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"
)

// UDPSender is the real Sender: one socket with SO_BROADCAST set, writing
// the magic packet to every local broadcast address.
//
// This is deliberately the same shape as what a FOG storage node already
// does in WakeOnLan::send -- 255.255.255.255 plus each interface's own
// broadcast address, UDP port 9. The agent is not doing anything a node
// does not; it is doing it from a link where FOG owns no node.
type UDPSender struct {
	conn net.PacketConn
	port int
}

// errNoSocket is a sender whose socket was already closed, or never opened.
var errNoSocket = errors.New("no socket")

// NewUDPSender opens the broadcast socket.
//
// SO_BROADCAST has to be set explicitly, because Go leaves it off. How
// strictly that is enforced varies -- measured on this box, a current
// Linux kernel accepts a write to 255.255.255.255 without it, while
// Windows fails the send with WSAEACCES and the BSDs with EACCES -- so it
// is set unconditionally rather than where it is known to be needed.
//
// It goes through ListenConfig.Control, which runs on the raw descriptor
// after the socket exists and before it is bound: the only window in which
// the option can be applied to a net.PacketConn.
func NewUDPSender(ctx context.Context) (*UDPSender, error) {
	lc := net.ListenConfig{Control: allowBroadcast}
	conn, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("wake socket: %w", err)
	}

	return &UDPSender{conn: conn, port: Port}, nil
}

// Close releases the socket.
func (s *UDPSender) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	conn := s.conn
	s.conn = nil

	return conn.Close()
}

// Broadcasts is every address a magic packet should be written to.
func (s *UDPSender) Broadcasts() ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	return broadcastsOf(ifaces), nil
}

// Send writes one datagram.
func (s *UDPSender) Send(dst net.IP, payload []byte) error {
	if s == nil || s.conn == nil {
		return errNoSocket
	}
	// A socket that will not write must not hold the poll open.
	if err := s.conn.SetWriteDeadline(time.Now().Add(Timeout)); err != nil {
		return err
	}
	_, err := s.conn.WriteTo(
		payload,
		&net.UDPAddr{IP: dst, Port: s.port},
	)

	return err
}

// allowBroadcast turns SO_BROADCAST on for a socket being opened.
func allowBroadcast(network, address string, c syscall.RawConn) error {
	var opErr error
	if err := c.Control(func(fd uintptr) {
		opErr = setBroadcast(fd)
	}); err != nil {
		return err
	}

	return opErr
}

// broadcastsOf is where packets go, given this machine's interfaces.
//
// Split from Broadcasts so the rule -- which interfaces count, and what a
// link's broadcast address is -- can be tested against interfaces this
// machine does not have.
func broadcastsOf(ifaces []net.Interface) []net.IP {
	// The limited broadcast is always in, and always first. It is the one
	// address that works when an interface reports no usable prefix at
	// all, which is what a machine on a /32 DHCP lease looks like.
	out := []net.IP{net.IPv4bcast}
	seen := map[string]bool{net.IPv4bcast.String(): true}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		// A packet on the loopback wakes nothing, and a link with no
		// broadcast (a tunnel, a point-to-point WAN link) has nowhere
		// to put one.
		if iface.Flags&net.FlagLoopback != 0 ||
			iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			// One unreadable interface is not a reason to send
			// nothing on the others.
			continue
		}
		for _, addr := range addrs {
			bcast := broadcastFor(addr)
			if bcast == nil || seen[bcast.String()] {
				continue
			}
			seen[bcast.String()] = true
			out = append(out, bcast)
		}
	}

	return out
}

// broadcastFor is a link's broadcast address, or nil when it has none.
func broadcastFor(addr net.Addr) net.IP {
	ipnet, ok := addr.(*net.IPNet)
	if !ok {
		return nil
	}
	ip := ipnet.IP.To4()
	if ip == nil {
		// IPv6 has no broadcast at all -- multicast replaced it -- and
		// Wake-on-LAN is not defined over it.
		return nil
	}
	mask := ipnet.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if len(mask) != net.IPv4len {
		return nil
	}
	ones, bits := ipnet.Mask.Size()
	if bits == 0 {
		// A non-contiguous mask. Size() reports 0,0 and there is no
		// prefix to reason about.
		return nil
	}
	if len(ipnet.Mask) == net.IPv6len {
		ones -= 96
	}
	// A /31 is a point-to-point pair (RFC 3021) and a /32 is a host
	// route; neither has a broadcast address to send to.
	if ones >= 31 {
		return nil
	}

	bcast := make(net.IP, net.IPv4len)
	for i := range bcast {
		bcast[i] = ip[i] | ^mask[i]
	}

	return bcast
}

// String names the socket, for a log line.
func (s *UDPSender) String() string {
	if s == nil || s.conn == nil {
		return "wake sender (closed)"
	}

	return "wake sender on " + s.conn.LocalAddr().String() +
		" -> udp/" + strconv.Itoa(s.port)
}
