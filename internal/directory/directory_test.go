package directory

import "testing"

func TestUnjoinedCarriesNothingElse(t *testing.T) {
	// A stale domain left on an unjoined machine is the half-answer that
	// makes a drift report untrustworthy: the server would compare a
	// leftover against the desired value and see agreement.
	d := Directory{
		Joined:         false,
		Kind:           KindWorkgroup,
		Domain:         "corp.example.com",
		Netbios:        "CORP",
		ComputerDN:     "CN=WS-014,OU=Sales,DC=corp,DC=example,DC=com",
		MachineAccount: "WS-014$",
		Site:           "HQ",
	}.normalize()

	if d.Domain != "" || d.Netbios != "" || d.ComputerDN != "" ||
		d.MachineAccount != "" || d.Site != "" {
		t.Errorf("unjoined kept membership detail: %+v", d)
	}
	if d.Kind != KindWorkgroup {
		t.Errorf("kind=%q: an explicit kind must survive", d.Kind)
	}
}

func TestUnjoinedWithNoKindGetsOne(t *testing.T) {
	if d := (Directory{Joined: false}).normalize(); d.Kind != KindNone {
		t.Errorf("kind=%q, want %q", d.Kind, KindNone)
	}
}

func TestMachineAccountGetsItsDollar(t *testing.T) {
	// Some sources give the dollar and some do not. A server comparing
	// WS-014 against WS-014$ would report drift that is not there.
	d := Directory{Joined: true, Kind: KindAD, Domain: "c.example.com",
		MachineAccount: "WS-014"}.normalize()
	if d.MachineAccount != "WS-014$" {
		t.Errorf("machine account %q", d.MachineAccount)
	}
	d.MachineAccount = "WS-014$"
	if got := d.normalize().MachineAccount; got != "WS-014$" {
		t.Errorf("a dollar was doubled: %q", got)
	}
}

func TestCaseIsNormalized(t *testing.T) {
	// The comparison on the server is a plain equality, so the folding has
	// to happen exactly once and it happens here.
	d := Directory{Joined: true, Kind: "AD", Domain: "CORP.Example.COM",
		Netbios: "corp"}.normalize()
	if d.Kind != KindAD || d.Domain != "corp.example.com" || d.Netbios != "CORP" {
		t.Errorf("got %+v", d)
	}
}

func TestDNIsNotCaseFolded(t *testing.T) {
	// A DN is handed back to the directory verbatim in a Modify DN. Folding
	// its case would be corrupting an identifier, not normalizing a name.
	dn := "CN=WS-014,OU=Sales,DC=corp,DC=example,DC=com"
	d := Directory{Joined: true, Kind: KindAD, Domain: "corp.example.com",
		ComputerDN: dn}.normalize()
	if d.ComputerDN != dn {
		t.Errorf("computer DN was rewritten: %q", d.ComputerDN)
	}
}

func TestHashChangesWithTheOU(t *testing.T) {
	// The whole point of the block: a machine that moved between OUs must
	// resend. If the hash missed the DN, a move would never be reported.
	a := Directory{Joined: true, Kind: KindAD, Domain: "corp.example.com",
		ComputerDN: "CN=WS-014,OU=Sales,DC=corp,DC=example,DC=com"}
	b := a
	b.ComputerDN = "CN=WS-014,OU=Engineering,DC=corp,DC=example,DC=com"
	if a.Hash() == b.Hash() {
		t.Fatal("moving between OUs did not change the hash")
	}
}

func TestHashIsStable(t *testing.T) {
	// And the inverse: an unchanged machine must not resend every hour.
	a := Directory{Joined: true, Kind: KindAD, Domain: "corp.example.com",
		Netbios: "CORP", ComputerDN: "CN=WS-014,DC=corp,DC=example,DC=com",
		MachineAccount: "WS-014$", Site: "HQ"}
	if a.Hash() != a.Hash() {
		t.Fatal("the hash is not stable across calls")
	}
}
