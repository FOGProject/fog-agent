package wake

import (
	"errors"
	"net"
	"strings"
	"testing"
)

// sentPacket is one datagram a fake Sender was asked to put on the wire.
type sentPacket struct {
	dst     string
	payload []byte
}

// fakeSender records what would have gone out. The whole point of the
// Sender interface is that a test can assert on the exact bytes and the
// exact destinations without a network.
type fakeSender struct {
	dsts     []net.IP
	dstErr   error
	sendErr  error
	failFrom int
	sent     []sentPacket
}

func (f *fakeSender) Broadcasts() ([]net.IP, error) {
	return f.dsts, f.dstErr
}

func (f *fakeSender) Send(dst net.IP, payload []byte) error {
	if f.sendErr != nil && len(f.sent) >= f.failFrom {
		return f.sendErr
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	f.sent = append(f.sent, sentPacket{dst: dst.String(), payload: cp})
	return nil
}

// broadcaster is a sender with two ordinary broadcast addresses.
func broadcaster() *fakeSender {
	return &fakeSender{
		dsts: []net.IP{
			net.ParseIP("10.255.20.255"),
			net.ParseIP("255.255.255.255"),
		},
	}
}

func TestRunSendsTheMagicPacketAndNothingElse(t *testing.T) {
	s := broadcaster()
	reports := Run(s, Policy{Targets: []Target{
		{ID: 41, MACs: []string{"00:11:22:33:44:55"}},
	}})

	if len(reports) != 1 || reports[0].Status != StatusSent {
		t.Fatalf("want one sent report, got %+v", reports)
	}
	if reports[0].Target.ID != 41 {
		t.Errorf("the report must name the host the server asked about, got %d",
			reports[0].Target.ID)
	}
	if len(s.sent) != 2 {
		t.Fatalf("want one packet per broadcast address, got %d", len(s.sent))
	}
	if reports[0].Packets != 2 {
		t.Errorf("the count must be the packets actually written, got %d",
			reports[0].Packets)
	}

	want := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		want = append(want, 0xFF)
	}
	mac := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}
	for i := 0; i < 16; i++ {
		want = append(want, mac...)
	}
	for _, p := range s.sent {
		if len(p.payload) != 102 {
			t.Fatalf("a magic packet is 102 bytes, got %d", len(p.payload))
		}
		if string(p.payload) != string(want) {
			t.Fatalf("the payload is not the magic packet for the given MAC")
		}
	}
}

// The security property this whole package exists to hold: the agent can
// only ever be made to talk to a broadcast address it computed itself.
func TestEveryDestinationComesFromTheSenderNotThePolicy(t *testing.T) {
	s := broadcaster()
	Run(s, Policy{Targets: []Target{{ID: 1, MACs: []string{"00:11:22:33:44:55"}}}})

	for _, p := range s.sent {
		if p.dst != "10.255.20.255" && p.dst != "255.255.255.255" {
			t.Fatalf("a packet went somewhere the sender did not offer: %s", p.dst)
		}
	}
}

func TestRunSendsForEveryMACOfATarget(t *testing.T) {
	s := broadcaster()
	reports := Run(s, Policy{Targets: []Target{
		{ID: 7, MACs: []string{"00:11:22:33:44:55", "00-11-22-33-44-56"}},
	}})

	if reports[0].Packets != 4 {
		t.Fatalf("two MACs across two broadcasts is four packets, got %d",
			reports[0].Packets)
	}
}

func TestDuplicateMACsAreSentOnce(t *testing.T) {
	s := broadcaster()
	// The same address in three of FOG's spellings. A host row that
	// accumulated all three over twenty years must not triple the traffic.
	reports := Run(s, Policy{Targets: []Target{
		{ID: 7, MACs: []string{
			"00:11:22:33:44:55", "00-11-22-33-44-55", "001122334455",
		}},
	}})

	if reports[0].Packets != 2 {
		t.Fatalf("one distinct MAC across two broadcasts is two packets, got %d",
			reports[0].Packets)
	}
}

func TestATargetWithNoUsableMACFailsRatherThanGoesQuiet(t *testing.T) {
	s := broadcaster()
	reports := Run(s, Policy{Targets: []Target{
		{ID: 3, MACs: []string{"not-a-mac"}},
	}})

	if len(reports) != 1 {
		t.Fatalf("the server asked about a host; it gets an answer. got %d", len(reports))
	}
	if reports[0].Status != StatusFailed {
		t.Errorf("want %s, got %s", StatusFailed, reports[0].Status)
	}
	if !strings.Contains(reports[0].Error, "not-a-mac") {
		t.Errorf("the failure must name the value that was rejected, got %q",
			reports[0].Error)
	}
	if len(s.sent) != 0 {
		t.Fatalf("nothing may go on the wire for an unparseable MAC")
	}
}

func TestAGoodMACSurvivesABadOneOnTheSameHost(t *testing.T) {
	s := broadcaster()
	reports := Run(s, Policy{Targets: []Target{
		{ID: 3, MACs: []string{"garbage", "00:11:22:33:44:55"}},
	}})

	if reports[0].Status != StatusSent || reports[0].Packets != 2 {
		t.Fatalf("one junk row must not cost the host its wake, got %+v", reports[0])
	}
}

func TestNoBroadcastAddressIsReportedPerTarget(t *testing.T) {
	s := &fakeSender{}
	reports := Run(s, Policy{Targets: []Target{
		{ID: 1, MACs: []string{"00:11:22:33:44:55"}},
		{ID: 2, MACs: []string{"00:11:22:33:44:56"}},
	}})

	if len(reports) != 2 {
		t.Fatalf("every target the server asked about needs an answer, got %d",
			len(reports))
	}
	for _, r := range reports {
		if r.Status != StatusFailed {
			t.Errorf("host %d: want %s, got %s", r.Target.ID, StatusFailed, r.Status)
		}
		if r.Error == "" {
			t.Errorf("host %d: a failure with no reason is what this replaces",
				r.Target.ID)
		}
	}
}

func TestAnInterfaceReadFailureSaysSo(t *testing.T) {
	s := &fakeSender{dstErr: errors.New("permission denied")}
	reports := Run(s, Policy{Targets: []Target{
		{ID: 1, MACs: []string{"00:11:22:33:44:55"}},
	}})

	if reports[0].Status != StatusFailed {
		t.Fatalf("want %s, got %s", StatusFailed, reports[0].Status)
	}
	if !strings.Contains(reports[0].Error, "permission denied") {
		t.Errorf("the underlying reason must survive, got %q", reports[0].Error)
	}
}

func TestASocketThatRefusesEveryWriteIsAFailure(t *testing.T) {
	s := broadcaster()
	s.sendErr = errors.New("network is unreachable")
	reports := Run(s, Policy{Targets: []Target{
		{ID: 1, MACs: []string{"00:11:22:33:44:55"}},
	}})

	if reports[0].Status != StatusFailed {
		t.Fatalf("want %s, got %s", StatusFailed, reports[0].Status)
	}
	if !strings.Contains(reports[0].Error, "unreachable") {
		t.Errorf("the socket's own words are the useful ones, got %q",
			reports[0].Error)
	}
}

func TestOneBroadcastFailingDoesNotSinkTheOthers(t *testing.T) {
	s := broadcaster()
	s.sendErr = errors.New("network is unreachable")
	s.failFrom = 1 // the first write lands, the rest do not
	reports := Run(s, Policy{Targets: []Target{
		{ID: 1, MACs: []string{"00:11:22:33:44:55"}},
	}})

	if reports[0].Status != StatusSent {
		t.Fatalf("a packet reached a link, so the wake was sent, got %s",
			reports[0].Status)
	}
	if reports[0].Packets != 1 {
		t.Errorf("the count must be what landed, not what was attempted, got %d",
			reports[0].Packets)
	}
}

func TestNoTargetsIsNoWork(t *testing.T) {
	s := broadcaster()
	if reports := Run(s, Policy{}); reports != nil {
		t.Fatalf("an empty block is not an instruction, got %+v", reports)
	}
	if len(s.sent) != 0 {
		t.Fatalf("nothing may go on the wire for an empty block")
	}
}

// MaxTargets is a constant here and not a number the server supplies. An
// agent whose traffic ceiling is set by whatever answers its poll has no
// ceiling.
func TestTheTargetListIsBoundedByTheAgent(t *testing.T) {
	s := broadcaster()
	targets := make([]Target, MaxTargets+10)
	for i := range targets {
		targets[i] = Target{ID: i + 1, MACs: []string{"00:11:22:33:44:55"}}
	}
	reports := Run(s, Policy{Targets: targets})

	if len(reports) != MaxTargets {
		t.Fatalf("want at most %d, got %d", MaxTargets, len(reports))
	}
	if len(s.sent) != MaxTargets*2 {
		t.Fatalf("want %d packets, got %d", MaxTargets*2, len(s.sent))
	}
}

func TestParseMACTakesEveryFormFOGStores(t *testing.T) {
	// MACAddress::PATTERN accepts all of these, so a row written by any of
	// FOG's twenty years of entry points has to survive the trip.
	for _, raw := range []string{
		"00:11:22:33:44:55",
		"00-11-22-33-44-55",
		"001122334455",
		"0011.2233.4455",
		"  00:11:22:33:44:55  ",
		"00:11:22:33:44:AA",
	} {
		mac, err := parseMAC(raw)
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if len(mac) != 6 {
			t.Errorf("%q: want 6 bytes, got %d", raw, len(mac))
		}
	}
}

func TestParseMACRejectsWhatIsNotASixByteAddress(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"not-a-mac",
		"00:11:22:33:44",          // five
		"00:11:22:33:44:55:66",    // seven
		"0011223344",              // ten hex digits
		"00112233445566",          // fourteen
		"00:11:22:33:44:GG",       // not hex
		"01:23:45:67:89:ab:cd:ef", // EUI-64: net.ParseMAC takes it, we must not
		"$(reboot)",
	} {
		if mac, err := parseMAC(raw); err == nil {
			t.Errorf("%q was accepted as %s", raw, mac)
		}
	}
}
