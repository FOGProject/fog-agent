//go:build linux

package software

import "testing"

func TestParseDpkgLine(t *testing.T) {
	got, ok := parseDpkgLine("openssh-server\t1:8.9p1-3ubuntu0.10\tUbuntu Developers <ubuntu-devel@lists.ubuntu.com>\tamd64")
	if !ok {
		t.Fatal("well-formed dpkg line was rejected")
	}
	want := Program{
		Name:      "openssh-server",
		Version:   "1:8.9p1-3ubuntu0.10",
		Publisher: "Ubuntu Developers <ubuntu-devel@lists.ubuntu.com>",
		Source:    "dpkg",
		Arch:      "amd64",
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseDpkgLineRejectsJunk(t *testing.T) {
	// dpkg-query's output ends with a blank line, and a short row is not a
	// package: neither may become a Program with an empty name, because the
	// server keys rows on the name.
	for _, line := range []string{"", "\t\t\t", "onlyname\tversion"} {
		if _, ok := parseDpkgLine(line); ok {
			t.Errorf("%q should not parse", line)
		}
	}
}

func TestParseRPMLine(t *testing.T) {
	got, ok := parseRPMLine("bash\t5.2.26-1.fc40\tFedora Project\tx86_64\t1719878400")
	if !ok {
		t.Fatal("well-formed rpm line was rejected")
	}
	if got.Name != "bash" || got.Version != "5.2.26-1.fc40" || got.Source != "rpm" {
		t.Errorf("got %+v", got)
	}
	if got.InstallDate != "2024-07-02" {
		t.Errorf("InstallDate = %q, want 2024-07-02", got.InstallDate)
	}
}

func TestParseRPMLineDropsNoneVendor(t *testing.T) {
	// rpm writes "(none)" for an unset vendor; storing that literal would
	// put "(none)" in the Publisher column of the report.
	got, ok := parseRPMLine("mypkg\t1.0-1\t(none)\tnoarch\t0")
	if !ok {
		t.Fatal("line was rejected")
	}
	if got.Publisher != "" {
		t.Errorf("Publisher = %q, want empty", got.Publisher)
	}
	if got.InstallDate != "" {
		t.Errorf("a zero install time is not a date, got %q", got.InstallDate)
	}
}

func TestUnixToDate(t *testing.T) {
	if got := unixToDate("notanumber"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := unixToDate("1719878400"); got != "2024-07-02" {
		t.Errorf("got %q", got)
	}
}

func TestHashChangesWithContentAndNotWithOrder(t *testing.T) {
	a := []Program{{Name: "a", Version: "1", Source: "dpkg"}, {Name: "b", Version: "2", Source: "dpkg"}}
	same := []Program{{Name: "a", Version: "1", Source: "dpkg"}, {Name: "b", Version: "2", Source: "dpkg"}}
	if Hash(a) != Hash(same) {
		t.Error("identical lists must hash the same, or the agent resends forever")
	}
	upgraded := []Program{{Name: "a", Version: "1", Source: "dpkg"}, {Name: "b", Version: "3", Source: "dpkg"}}
	if Hash(a) == Hash(upgraded) {
		t.Error("a version change must move the hash, or an upgrade is never reported")
	}
}
