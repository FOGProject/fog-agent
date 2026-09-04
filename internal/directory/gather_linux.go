//go:build linux

package directory

import (
	"os"
	"os/exec"
	"strings"
)

// The probes, in the order they are tried, and the sssd config path. Both
// are variables so the tests drive the parsers against captured output
// rather than against whatever this build machine happens to be joined to.
var (
	runRealmList = func() (string, bool) {
		out, err := exec.Command("realm", "list").Output()
		if err != nil {
			return "", false
		}
		return string(out), true
	}
	sssdConf = "/etc/sssd/sssd.conf"
)

// gather answers from realmd first and sssd's own configuration second.
//
// The order is deliberate: realmd knows whether a join actually completed,
// where sssd.conf only records what someone configured. A machine with a
// domain in sssd.conf that never joined would otherwise report as a member,
// and design 0009's whole point is telling the intended apart from the real.
//
// A machine with neither is not domain-joined, and saying so is an answer.
// That is different from Windows, where a probe that errors could mean
// anything -- here the absence of every join mechanism Linux has is itself
// the evidence.
func gather() (Directory, bool) {
	if out, ok := runRealmList(); ok {
		if d, ok := parseRealmList(out); ok {
			return withMachineAccount(d), true
		}
		// realmd ran and listed nothing configured: a positive "not joined".
		return Directory{Joined: false, Kind: KindNone}, true
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
	d.MachineAccount = strings.ToUpper(short)
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
