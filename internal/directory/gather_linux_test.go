//go:build linux

package directory

import "testing"

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

func TestGatherUsesRealmBeforeSSSD(t *testing.T) {
	// realmd knows whether a join completed; sssd.conf only records what
	// someone configured. A machine configured for a domain it never
	// joined must report unjoined.
	orig := runRealmList
	defer func() { runRealmList = orig }()
	runRealmList = func() (string, bool) { return realmDiscoveredOnly, true }

	d, ok := gather()
	if !ok {
		t.Fatal("gather reported no collector ran")
	}
	if d.Joined {
		t.Error("a discovered-but-unjoined realm reported as joined")
	}
}
