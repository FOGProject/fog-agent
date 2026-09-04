//go:build linux

package directory

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// realmJoined is `realm list` on an AD-joined machine.
//
// The shape is not invented. The printer is `  %s: %s` and the block header
// is `   %s`, both read out of /usr/bin/realm itself; the key names
// (configured, domain-name, realm-name, server-software, client-software,
// login-formats) are the literal strings in that binary; and the values
// kerberos-member, active-directory and sssd are the literal strings in
// /usr/libexec/realmd. Captured that way because this box is not joined to
// anything and there is no domain in the lab to join it to -- a fixture
// written from memory would share whatever the parser got wrong.
const realmJoined = `corp.example.com
  type: kerberos
  realm-name: CORP.EXAMPLE.COM
  domain-name: corp.example.com
  configured: kerberos-member
  server-software: active-directory
  client-software: sssd
  required-package: sssd-tools
  login-formats: %U@corp.example.com
  login-policy: allow-realm-logins
`

// realmDiscoveredOnly is `realm list --all` where the realm was found but
// never joined. The default `realm list` does not show these at all (man
// realm, LIST), so the parser has to handle both.
const realmDiscoveredOnly = `corp.example.com
  type: kerberos
  realm-name: CORP.EXAMPLE.COM
  domain-name: corp.example.com
  configured: no
  server-software: active-directory
  client-software: sssd
`

func TestRealmListJoined(t *testing.T) {
	d, ok := parseRealmList(realmJoined)
	if !ok {
		t.Fatal("a configured realm did not parse as joined")
	}
	if !d.Joined || d.Kind != KindAD {
		t.Errorf("joined=%v kind=%q, want true/%q", d.Joined, d.Kind, KindAD)
	}
	if d.Domain != "corp.example.com" {
		t.Errorf("domain=%q", d.Domain)
	}
	if d.Netbios != "" {
		t.Errorf("netbios=%q: realm list does not report one and it is not"+
			" derivable from the DNS name, so it must stay empty", d.Netbios)
	}
}

func TestRealmListDiscoveredIsNotJoined(t *testing.T) {
	// The distinction the whole design turns on: a realm someone could
	// join is not a realm this machine is in. Reporting it as a membership
	// would put every host that can see a DC into a domain it never joined.
	if _, ok := parseRealmList(realmDiscoveredOnly); ok {
		t.Fatal("configured: no parsed as a membership")
	}
}

func TestRealmListEmptyIsNotJoined(t *testing.T) {
	// This machine's real output: realm is installed, exits 0, prints
	// nothing. Captured from the box, not imagined.
	if _, ok := parseRealmList(""); ok {
		t.Fatal("empty realm list parsed as a membership")
	}
}

func TestNonADRealmIsNotClaimedAsAD(t *testing.T) {
	// An IPA or plain Kerberos realm is a real membership with no computer
	// object design 0009 §5 could move. Reporting kind=ad would put it in
	// the drift report as a movable host it is not.
	ipa := `ipa.example.com
  type: kerberos
  realm-name: IPA.EXAMPLE.COM
  domain-name: ipa.example.com
  configured: kerberos-member
  server-software: freeipa
  client-software: sssd
`
	d, ok := parseRealmList(ipa)
	if !ok {
		t.Fatal("a configured IPA realm should still report as joined")
	}
	if d.Kind == KindAD {
		t.Error("an IPA realm was reported as kind=ad")
	}
}

func TestSSSDConfADDomain(t *testing.T) {
	conf := `[sssd]
domains = corp.example.com
services = nss, pam

[domain/corp.example.com]
id_provider = ad
ad_domain = corp.example.com
krb5_realm = CORP.EXAMPLE.COM
`
	d, ok := parseSSSDConf(conf)
	if !ok {
		t.Fatal("an ad id_provider did not parse")
	}
	if !d.Joined || d.Kind != KindAD || d.Domain != "corp.example.com" {
		t.Errorf("got %+v", d)
	}
}

func TestSSSDConfSectionHeaderIsTheFallbackDomain(t *testing.T) {
	conf := `[domain/corp.example.com]
id_provider = ad
`
	d, ok := parseSSSDConf(conf)
	if !ok || d.Domain != "corp.example.com" {
		t.Errorf("got %+v ok=%v, want the domain from the section header", d, ok)
	}
}

func TestSSSDConfNonADProviderIsNotAMembership(t *testing.T) {
	conf := `[domain/example]
id_provider = ldap
ldap_uri = ldap://example.com
`
	if _, ok := parseSSSDConf(conf); ok {
		t.Fatal("an ldap id_provider parsed as a directory membership")
	}
}

func TestSSSDCommentsIgnored(t *testing.T) {
	conf := `# id_provider = ad
; ad_domain = commented.example.com
[domain/real.example.com]
id_provider = ad
`
	d, ok := parseSSSDConf(conf)
	if !ok || d.Domain != "real.example.com" {
		t.Errorf("got %+v ok=%v: a commented-out setting was read", d, ok)
	}
}

// isolate points every probe at nothing, so a test says what it means on a
// machine that is itself joined to something. Without it these tests read
// this build box's /etc and pass or fail for reasons of their own.
func isolate(t *testing.T) {
	t.Helper()
	realm, keytab, conf := runRealmList, hostKeytab, sssdConf
	t.Cleanup(func() { runRealmList, hostKeytab, sssdConf = realm, keytab, conf })
	runRealmList = func() (string, bool) { return "", false }
	hostKeytab = filepath.Join(t.TempDir(), "no-such-keytab")
	sssdConf = filepath.Join(t.TempDir(), "no-such-sssd.conf")
}

// keytabEntry builds one 0x0502 entry for a principal, with a one-byte key
// standing in for the key material the parser never reads.
func keytabEntry(realm string, components ...string) []byte {
	counted := func(s string) []byte {
		b := make([]byte, 2+len(s))
		binary.BigEndian.PutUint16(b, uint16(len(s)))
		copy(b[2:], s)
		return b
	}
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, uint16(len(components)))
	body = append(body, counted(realm)...)
	for _, c := range components {
		body = append(body, counted(c)...)
	}
	body = append(body, 0, 0, 0, 1) // name type
	body = append(body, 0, 0, 0, 0) // timestamp
	body = append(body, 3)          // key version
	body = append(body, 0, 18)      // enctype aes256-cts-hmac-sha1-96
	body = append(body, counted("k")...)

	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	return append(out, body...)
}

func keytab(entries ...[]byte) []byte {
	out := []byte{0x05, 0x02}
	for _, e := range entries {
		out = append(out, e...)
	}
	return out
}

func TestKeytabMachineAccountIsAJoin(t *testing.T) {
	// The shape adcli writes: host principals plus the computer object's
	// own sAMAccountName. Confirmed against the real /etc/krb5.keytab an
	// adcli join left on the lab VM, 2026-09-04.
	d, ok := parseKeytab(keytab(
		keytabEntry("FOGAD.LAB", "host", "fogagent-vm.fogad.lab"),
		keytabEntry("FOGAD.LAB", "FOGAGENT-VM$"),
	))
	if !ok {
		t.Fatal("a machine account keytab did not read as a join")
	}
	if !d.Joined || d.Kind != KindAD {
		t.Errorf("got joined=%v kind=%q, want an AD membership", d.Joined, d.Kind)
	}
	if d.Domain != "fogad.lab" {
		t.Errorf("domain %q: the realm should be reported lowercased", d.Domain)
	}
	if d.MachineAccount != "FOGAGENT-VM$" {
		t.Errorf("machine account %q, want FOGAGENT-VM$", d.MachineAccount)
	}
}

func TestKeytabWithoutAMachineAccountIsNotAJoin(t *testing.T) {
	// A plain MIT host keytab, and a one-component principal that is not a
	// machine account -- what `ktutil add` writes for a user or a service.
	// The dollar is the only thing separating these from a domain join, so
	// both have to be rejected: claiming membership here would send the
	// server looking for a computer object that does not exist.
	for _, tc := range []struct {
		name       string
		components []string
	}{
		{"a host principal", []string{"host", "web01.example.com"}},
		{"a one-component principal with no dollar", []string{"admin"}},
	} {
		if d, ok := parseKeytab(keytab(
			keytabEntry("EXAMPLE.COM", tc.components...),
		)); ok {
			t.Errorf("%s: got %+v, read as a domain join", tc.name, d)
		}
	}
}

func TestKeytabDeletedEntryIsSkipped(t *testing.T) {
	// A removed entry keeps its slot with a negative length -- which is how
	// a keytab records a machine account superseded by a re-join. Reading
	// the hole as live reports the OLD account, so the entry left behind
	// has to differ from the live one for this to prove anything.
	stale := keytabEntry("OLD.EXAMPLE.COM", "GONE-VM$")
	binary.BigEndian.PutUint32(stale, uint32(-int32(len(stale)-4)))
	live := keytabEntry("FOGAD.LAB", "FOGAGENT-VM$")
	d, ok := parseKeytab(keytab(stale, live))
	if !ok || d.MachineAccount != "FOGAGENT-VM$" || d.Domain != "fogad.lab" {
		t.Errorf("got %+v ok=%v, want the live entry, not the hole", d, ok)
	}
}

func TestKeytabTruncatedIsNotAJoin(t *testing.T) {
	full := keytab(keytabEntry("FOGAD.LAB", "FOGAGENT-VM$"))
	for _, n := range []int{0, 1, 2, 5, len(full) - 3} {
		if d, ok := parseKeytab(full[:n]); ok {
			t.Errorf("%d bytes: got %+v, want no answer", n, d)
		}
	}
}

func TestGatherUsesRealmBeforeSSSD(t *testing.T) {
	// realmd knows whether a join completed; sssd.conf only records what
	// someone configured. A machine configured for a domain it never
	// joined must report unjoined.
	isolate(t)
	runRealmList = func() (string, bool) { return realmDiscoveredOnly, true }

	d, ok := gather()
	if !ok {
		t.Fatal("gather reported no collector ran")
	}
	if d.Joined {
		t.Error("a discovered-but-unjoined realm reported as joined")
	}
}

func TestGatherReadsTheKeytabWhenRealmdKnowsNothing(t *testing.T) {
	// The case the lab found: the agent's own adcli join leaves realmd
	// with nothing to list and writes no sssd.conf, so without the keytab
	// the machine reports unjoined and the server re-sends the credential
	// for ever.
	isolate(t)
	runRealmList = func() (string, bool) { return realmDiscoveredOnly, true }
	path := filepath.Join(t.TempDir(), "krb5.keytab")
	if err := os.WriteFile(path, keytab(keytabEntry("FOGAD.LAB", "FOGAGENT-VM$")), 0o600); err != nil {
		t.Fatal(err)
	}
	hostKeytab = path

	d, ok := gather()
	if !ok {
		t.Fatal("gather reported no collector ran")
	}
	if !d.Joined || d.Domain != "fogad.lab" {
		t.Errorf("got %+v, want a join to fogad.lab from the keytab", d)
	}
}

func TestGatherPrefersSSSDOverNothing(t *testing.T) {
	// The keytab is missing but sssd is configured: still an answer, and
	// the ordering must not have broken the fallback.
	isolate(t)
	path := filepath.Join(t.TempDir(), "sssd.conf")
	if err := os.WriteFile(path, []byte("[domain/corp.example.com]\nid_provider = ad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sssdConf = path

	d, ok := gather()
	if !ok || !d.Joined || d.Domain != "corp.example.com" {
		t.Errorf("got %+v ok=%v, want the sssd domain", d, ok)
	}
}

func TestGatherWithNoEvidenceIsAPositiveNotJoined(t *testing.T) {
	isolate(t)
	d, ok := gather()
	if !ok {
		t.Fatal("gather reported no collector ran; it should answer")
	}
	if d.Joined || d.Kind != KindNone {
		t.Errorf("got %+v, want a positive not-joined", d)
	}
}
