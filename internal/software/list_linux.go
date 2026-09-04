//go:build linux

package software

import (
	"os/exec"
	"strings"
	"time"
)

// list asks whichever package managers are present what they have installed.
// A machine can carry more than one (a dpkg host with flatpaks), so every
// manager found contributes rows.
//
// The bool reports whether any manager actually ran. It is false when none
// is installed, which is not the same as a machine with no packages: the
// server treats a reported list as complete and marks everything absent from
// it as removed, so "no collector" must stay silent rather than send [].
func list() ([]Program, bool) {
	var out []Program
	ran := false
	for _, m := range []struct {
		bin   string
		parse func(string) (Program, bool)
		args  []string
	}{
		{"dpkg-query", parseDpkgLine, []string{"-W", "-f=${Package}\t${Version}\t${Maintainer}\t${Architecture}\n"}},
		{"rpm", parseRPMLine, []string{"-qa", "--qf", "%{NAME}\t%{VERSION}-%{RELEASE}\t%{VENDOR}\t%{ARCH}\t%{INSTALLTIME}\n"}},
	} {
		progs, ok := fromCommand(m.bin, m.parse, m.args...)
		if ok {
			ran = true
			out = append(out, progs...)
		}
	}
	return out, ran
}

// fromCommand runs a package manager if it is on PATH and turns each output
// line into a Program with the given parser. The bool is false when the
// manager is absent or failed, so the caller can tell "not installed" from
// "installed and reported nothing".
func fromCommand(bin string, parse func(string) (Program, bool), args ...string) ([]Program, bool) {
	if _, err := exec.LookPath(bin); err != nil {
		return nil, false
	}
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		return nil, false
	}
	var progs []Program
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := parse(line); ok {
			progs = append(progs, p)
		}
	}
	return progs, true
}

// parseDpkgLine reads one tab-separated dpkg-query row. Split out from the
// exec so the format is tested against a fixture, not against whatever this
// machine has installed.
func parseDpkgLine(line string) (Program, bool) {
	f := strings.Split(strings.TrimRight(line, "\r"), "\t")
	if len(f) < 4 || f[0] == "" {
		return Program{}, false
	}
	return Program{
		Name:      f[0],
		Version:   f[1],
		Publisher: f[2],
		Source:    "dpkg",
		Arch:      f[3],
	}, true
}

// parseRPMLine reads one tab-separated rpm -qa row. rpm reports the install
// time as a unix timestamp, which becomes the date the server stores.
func parseRPMLine(line string) (Program, bool) {
	f := strings.Split(strings.TrimRight(line, "\r"), "\t")
	if len(f) < 5 || f[0] == "" {
		return Program{}, false
	}
	p := Program{
		Name:      f[0],
		Version:   f[1],
		Publisher: f[2],
		Source:    "rpm",
		Arch:      f[3],
	}
	// "(none)" is rpm's way of saying a field is unset; it is not a vendor.
	if p.Publisher == "(none)" {
		p.Publisher = ""
	}
	p.InstallDate = unixToDate(f[4])
	return p, true
}

// unixToDate turns an epoch-seconds string into YYYY-MM-DD, or "" if it is
// not a timestamp.
func unixToDate(s string) string {
	var secs int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
		secs = secs*10 + int64(r-'0')
	}
	if secs == 0 {
		return ""
	}
	return time.Unix(secs, 0).UTC().Format("2006-01-02")
}
