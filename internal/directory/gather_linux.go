//go:build linux

package directory

import (
	"encoding/binary"
	"os"
	"os/exec"
	"strings"
)

// The probes, in the order they are tried, and the file paths they read.
// All are variables so the tests drive the parsers against captured output
// rather than against whatever this build machine happens to be joined to.
var (
	runRealmList = func() (string, bool) {
		out, err := exec.Command("realm", "list").Output()
		if err != nil {
			return "", false
		}
		return string(out), true
	}
	hostKeytab = "/etc/krb5.keytab"
	sssdConf   = "/etc/sssd/sssd.conf"
)

// gather answers from realmd, then the host keytab, then sssd's own
// configuration.
//
// The order is by how directly each one witnesses a join. realmd knows
// whether a join actually completed. The keytab is what the join itself
// WRITES -- a machine account key that only a domain controller could have
// issued -- so it is evidence, not intent. sssd.conf is last because it
// records only what someone configured: a machine with a domain in
// sssd.conf that never joined would otherwise report as a member, and
// design 0009's whole point is telling the intended apart from the real.
//
// The keytab probe is not optional politeness. The agent's own Linux join
// (design 0009 §6) runs adcli, and adcli joins the machine without
// configuring any name-service stack: no realmd, no sssd.conf. Measured
// 2026-09-04 against the lab DC -- the join succeeded, the computer object
// was created, `adcli testjoin` validated it, and this package reported
// `joined=false`. The server would then have believed the host unjoined
// forever and re-sent the join credential every hour, which is exactly the
// behavior 0009 §6 exists to stop.
//
// A machine with none of the three is not domain-joined, and saying so is
// an answer. That is different from Windows, where a probe that errors
// could mean anything -- here the absence of every join mechanism Linux has
// is itself the evidence.
func gather() (Directory, bool) {
	if out, ok := runRealmList(); ok {
		if d, ok := parseRealmList(out); ok {
			return withMachineAccount(d), true
		}
		// realmd ran and listed nothing configured. Not conclusive on its
		// own: an adcli join leaves realmd with nothing to list, so fall
		// through to the keytab rather than calling it a workgroup.
	}
	if body, err := os.ReadFile(hostKeytab); err == nil {
		if d, ok := parseKeytab(body); ok {
			return d, true
		}
	}
	if body, err := os.ReadFile(sssdConf); err == nil {
		if d, ok := parseSSSDConf(string(body)); ok {
			return withMachineAccount(d), true
		}
	}
	return Directory{Joined: false, Kind: KindNone}, true
}

// withMachineAccount fills in the account name from the machine's own short
// hostname, which is what every Linux join tool derives it from. No Linux
// join tool exposes the computer object's DN, so ComputerDN stays empty and
// the server searches for this name instead (design 0009 §3).
func withMachineAccount(d Directory) Directory {
	h, err := os.Hostname()
	if err != nil {
		return d
	}
	short, _, _ := strings.Cut(h, ".")
	if short == "" {
		return d
	}
	// With the trailing dollar, which is what the field is documented to
	// hold and what Windows reports (CORP\WS-014$ -> WS-014$). The server
	// normalizes either shape, but a report an admin reads should not show
	// the same machine two different ways depending on its OS.
	d.MachineAccount = strings.ToUpper(short) + "$"
	return d
}

// parseRealmList reads `realm list`. Each realm is a block whose first line
// is the domain, unindented, followed by indented `key: value` lines. The
// key that matters is `configured`, which is `no` for a realm realmd merely
// discovered and something else -- `kerberos-member` in the AD case -- for
// one this machine actually joined.
func parseRealmList(out string) (Directory, bool) {
	var (
		cur     string
		fields  map[string]string
		blocks  = map[string]map[string]string{}
		order   []string
		flushed = func() {
			if cur != "" {
				blocks[cur] = fields
				order = append(order, cur)
			}
		}
	)
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			flushed()
			cur = strings.TrimSpace(line)
			fields = map[string]string{}
			continue
		}
		if fields == nil {
			continue
		}
		k, v, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		fields[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	flushed()

	for _, name := range order {
		f := blocks[name]
		conf := f["configured"]
		if conf == "" || conf == "no" {
			continue
		}
		// server-software says what is on the other end. Only an
		// active-directory realm has a computer object design 0009 §5 can
		// move; an IPA or plain Kerberos realm is a membership this can
		// report but must not claim is AD.
		kind := KindAD
		if s := f["server-software"]; s != "" && s != "active-directory" {
			kind = KindNone
		}
		domain := f["domain-name"]
		if domain == "" {
			domain = name
		}
		return Directory{
			Joined: true,
			Kind:   kind,
			Domain: domain,
			// realm list does not report the NetBIOS name and it is not
			// derivable from the DNS name -- CORP is a convention, not a
			// rule. Left empty rather than guessed; the server matches on
			// the DNS name or the first label (design 0009 §3).
		}, true
	}
	return Directory{}, false
}

// parseKeytab reads an MIT/Heimdal host keytab and answers from the machine
// account principal in it.
//
// The file is parsed rather than shelled out to `klist -k` because a joined
// machine is not required to have klist installed -- adcli needs libkrb5,
// not the command-line tools -- and because a captured file is something a
// test can hold, where a command's output is something the test would have
// to believe.
//
// The discriminator is the trailing dollar. An AD join writes the computer
// object's sAMAccountName as a one-component principal, FOGAGENT-VM$@REALM,
// and nothing else does: an MIT or IPA host keytab holds host/fqdn@REALM
// and no dollar account. So a dollar principal means AD and gives the realm
// and the machine account with it; anything else returns false and lets the
// next probe answer, rather than claiming a Kerberos host is a domain
// member.
//
// Layout (both versions), big-endian in 0x0502 and native-endian in 0x0501:
//
//	uint16 0x0502
//	repeated: int32 size, then `size` bytes of entry (negative = a hole)
//	entry:    uint16 component count, counted realm, counted components...
//
// Only the principal is read; the key material is skipped, deliberately --
// this package reports facts about membership and has no business holding a
// machine's key in memory.
func parseKeytab(body []byte) (Directory, bool) {
	if len(body) < 2 || body[0] != 0x05 {
		return Directory{}, false
	}
	var order binary.ByteOrder = binary.BigEndian
	switch body[1] {
	case 0x02:
	case 0x01:
		// Version 1 wrote integers in the host's byte order. Every machine
		// this agent runs on is little-endian; a big-endian one reading a
		// v1 keytab it did not write is not a case worth guessing at.
		order = binary.LittleEndian
	default:
		return Directory{}, false
	}

	for at := 2; at+4 <= len(body); {
		size := int32(order.Uint32(body[at : at+4]))
		at += 4
		length := int(size)
		if size < 0 {
			// A deleted entry: the slot is still that many bytes wide.
			length = int(-size)
		}
		if length <= 0 || at+length > len(body) {
			return Directory{}, false
		}
		entry := body[at : at+length]
		at += length
		if size < 0 {
			continue
		}
		if d, ok := machineAccountIn(entry, order); ok {
			return d, true
		}
	}
	return Directory{}, false
}

// machineAccountIn reads one keytab entry's principal and reports it only
// when it is a machine account.
func machineAccountIn(entry []byte, order binary.ByteOrder) (Directory, bool) {
	counted := func(b []byte) (string, []byte, bool) {
		if len(b) < 2 {
			return "", nil, false
		}
		n := int(order.Uint16(b[:2]))
		if len(b) < 2+n {
			return "", nil, false
		}
		return string(b[2 : 2+n]), b[2+n:], true
	}

	if len(entry) < 2 {
		return Directory{}, false
	}
	count := int(order.Uint16(entry[:2]))
	rest := entry[2:]
	realm, rest, ok := counted(rest)
	if !ok || realm == "" {
		return Directory{}, false
	}
	// A machine account is one component. Reading the rest anyway would
	// mean parsing host/fqdn entries to throw them away.
	if count != 1 {
		return Directory{}, false
	}
	name, _, ok := counted(rest)
	if !ok || !strings.HasSuffix(name, "$") {
		return Directory{}, false
	}
	return Directory{
		Joined:         true,
		Kind:           KindAD,
		Domain:         strings.ToLower(realm),
		MachineAccount: strings.ToUpper(name),
		// The keytab holds no DN and no NetBIOS name. Left empty rather
		// than guessed; the server matches on MachineAccount (0009 §3).
	}, true
}

// parseSSSDConf reads the domain sections of sssd.conf, used only when
// realmd is absent. `id_provider = ad` is the marker; anything else (ldap,
// ipa, files) is a directory this cannot place a computer object in.
func parseSSSDConf(body string) (Directory, bool) {
	var (
		section  string
		provider string
		adDomain string
		secDom   string
	)
	commit := func() (Directory, bool) {
		if provider != "ad" {
			return Directory{}, false
		}
		domain := adDomain
		if domain == "" {
			domain = secDom
		}
		if domain == "" {
			return Directory{}, false
		}
		return Directory{Joined: true, Kind: KindAD, Domain: domain}, true
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if d, ok := commit(); ok {
				return d, true
			}
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			provider, adDomain, secDom = "", "", ""
			// `[domain/corp.example.com]` names the domain in the header,
			// which is the fallback when ad_domain is not spelled out.
			if rest, found := strings.CutPrefix(section, "domain/"); found {
				secDom = rest
			}
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "id_provider":
			provider = strings.ToLower(strings.TrimSpace(v))
		case "ad_domain":
			adDomain = strings.TrimSpace(v)
		}
	}
	return commit()
}
