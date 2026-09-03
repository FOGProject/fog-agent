package software

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// Every row of the convergence rule (design 0003 section 3).
func TestDecide(t *testing.T) {
	cases := []struct {
		name      string
		entry     Entry
		installed map[string]string
		want      Action
	}{
		{"present, any, missing", Entry{Package: "vlc", State: StatePresent}, nil, Install},
		{"present, any, installed", Entry{Package: "vlc", State: StatePresent}, map[string]string{"vlc": "3.0"}, None},
		{"present, latest, missing", Entry{Package: "vlc", State: StatePresent, Version: "latest"}, nil, Install},
		{"present, latest, installed", Entry{Package: "vlc", State: StatePresent, Version: "latest"}, map[string]string{"vlc": "3.0"}, Upgrade},
		{"present, pinned, missing", Entry{Package: "vlc", State: StatePresent, Version: "3.1"}, nil, Install},
		{"present, pinned, wrong version", Entry{Package: "vlc", State: StatePresent, Version: "3.1"}, map[string]string{"vlc": "3.0"}, Install},
		{"present, pinned, right version", Entry{Package: "vlc", State: StatePresent, Version: "3.1"}, map[string]string{"vlc": "3.1"}, None},
		{"absent, installed", Entry{Package: "vlc", State: StateAbsent}, map[string]string{"vlc": "3.0"}, Uninstall},
		{"absent, missing", Entry{Package: "vlc", State: StateAbsent}, nil, None},
		{"package id case-insensitive", Entry{Package: "GoogleChrome", State: StatePresent}, map[string]string{"googlechrome": "1"}, None},
	}
	for _, c := range cases {
		got, _ := Decide(c.entry, c.installed)
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// fake is a backend that records what it was asked and answers from a map.
type fake struct {
	available bool
	installed map[string]string
	listErr   error
	calls     []string
	exit      map[string]int
}

func (f *fake) Available() (bool, string) {
	if !f.available {
		return false, "choco is missing"
	}
	return true, ""
}
func (f *fake) Installed(context.Context) (map[string]string, error) {
	f.calls = append(f.calls, "list")
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := map[string]string{}
	for k, v := range f.installed {
		out[k] = v
	}
	return out, nil
}
func (f *fake) act(kind string, e Entry) Result {
	f.calls = append(f.calls, kind+":"+e.Package)
	code := f.exit[e.Package]
	if code == 0 {
		switch kind {
		case "uninstall":
			delete(f.installed, e.Package)
		default:
			v := e.Version
			if v == "" || v == VersionLatest {
				v = "9.9"
			}
			f.installed[e.Package] = v
		}
	}
	return Result{ExitCode: code, Details: kind + " ran"}
}
func (f *fake) Install(_ context.Context, e Entry) Result   { return f.act("install", e) }
func (f *fake) Upgrade(_ context.Context, e Entry) Result   { return f.act("upgrade", e) }
func (f *fake) Uninstall(_ context.Context, e Entry) Result { return f.act("uninstall", e) }

func TestConvergeRunsInOrderAndReportsVersions(t *testing.T) {
	b := &fake{available: true, installed: map[string]string{"old": "1.0", "vlc": "3.0"}, exit: map[string]int{"broken": 1603}}
	entries := []Entry{
		{ID: 1, Package: "vlc", State: StatePresent},                    // converged
		{ID: 2, Package: "7zip", State: StatePresent, Version: "22.0"},  // install pinned
		{ID: 3, Package: "old", State: StateAbsent},                     // uninstall
		{ID: 4, Package: "broken", State: StatePresent},                 // install fails
		{ID: 5, Package: "vlc", State: StatePresent, Version: "latest"}, // upgrade (dedupe is the server's job)
	}
	reports := Converge(context.Background(), b, entries)
	wantCalls := []string{"list", "install:7zip", "uninstall:old", "install:broken", "upgrade:vlc", "list"}
	if !reflect.DeepEqual(b.calls, wantCalls) {
		t.Fatalf("calls = %q, want %q", b.calls, wantCalls)
	}
	want := []struct {
		status, version string
		code            int
	}{
		{StatusConverged, "9.9", 0}, // re-read after the upgrade: the version the host ended with
		{StatusInstalled, "22.0", 0},
		{StatusRemoved, "", 0},
		{StatusInstalled, "", 1603},
		{StatusUpgraded, "9.9", 0},
	}
	for i, w := range want {
		r := reports[i]
		if r.Status != w.status || r.InstalledVersion != w.version || r.ExitCode != w.code {
			t.Errorf("report %d: got %s/%q/%d, want %s/%q/%d", i, r.Status, r.InstalledVersion, r.ExitCode, w.status, w.version, w.code)
		}
	}
}

// Tracking latest runs upgrade every time; when nothing newer exists the
// version does not move and the report says converged, not upgraded.
func TestConvergeUpgradeWithNothingNewerIsConverged(t *testing.T) {
	b := &fake{available: true, installed: map[string]string{"vlc": "9.9"}}
	reports := Converge(context.Background(), b, []Entry{{Package: "vlc", State: StatePresent, Version: VersionLatest}})
	r := reports[0]
	if r.Action != Upgrade || r.Status != StatusConverged || r.InstalledVersion != "9.9" {
		t.Errorf("got %s/%s/%q, want upgrade ran, converged, 9.9", r.Action, r.Status, r.InstalledVersion)
	}
	b = &fake{available: true, installed: map[string]string{"vlc": "1.0"}}
	if r := Converge(context.Background(), b, []Entry{{Package: "vlc", State: StatePresent, Version: VersionLatest}})[0]; r.Status != StatusUpgraded || r.InstalledVersion != "9.9" {
		t.Errorf("a real upgrade must still say upgraded: %s %q", r.Status, r.InstalledVersion)
	}
}

func TestConvergeWithoutActionsListsOnce(t *testing.T) {
	b := &fake{available: true, installed: map[string]string{"vlc": "3.0"}}
	Converge(context.Background(), b, []Entry{{Package: "vlc", State: StatePresent}})
	if !reflect.DeepEqual(b.calls, []string{"list"}) {
		t.Errorf("calls = %q, want one list", b.calls)
	}
}

func TestConvergeUnavailableBackend(t *testing.T) {
	b := &fake{available: false}
	reports := Converge(context.Background(), b, []Entry{{Package: "a"}, {Package: "b"}})
	if len(reports) != 2 || len(b.calls) != 0 {
		t.Fatalf("expected two cannot_run reports and no calls, got %d reports, calls %q", len(reports), b.calls)
	}
	for _, r := range reports {
		if r.Status != StatusCannotRun || r.Details != "choco is missing" {
			t.Errorf("got %s %q", r.Status, r.Details)
		}
	}
}

func TestConvergeListError(t *testing.T) {
	b := &fake{available: true, listErr: errors.New("boom")}
	reports := Converge(context.Background(), b, []Entry{{Package: "a"}})
	if reports[0].Status != StatusCannotRun || reports[0].Details != "installed list: boom" {
		t.Errorf("got %s %q", reports[0].Status, reports[0].Details)
	}
}

func TestParseList(t *testing.T) {
	out := "Chocolatey v2.2.2\nchocolatey|2.2.2\nGoogleChrome|118.0.5993.90\n\n2 packages installed.\n"
	got := ParseList(out)
	want := map[string]string{"chocolatey": "2.2.2", "googlechrome": "118.0.5993.90"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestChocoArgs(t *testing.T) {
	e := Entry{Package: "vlc", State: StatePresent, Version: "3.0.18", Source: `\\fs\choco`, Args: `--params "/NoDesktop"`}
	if got, want := InstallArgs(e), []string{"install", "vlc", "-y", "-r", "--no-progress", "--version=3.0.18", "--force", `--source=\\fs\choco`, "--params", "/NoDesktop"}; !reflect.DeepEqual(got, want) {
		t.Errorf("install: got %q, want %q", got, want)
	}
	e.Version = VersionLatest
	if got := InstallArgs(e); len(got) != 8 {
		t.Errorf("latest must not pin: %q", got)
	}
	if got, want := UpgradeArgs(e), []string{"upgrade", "vlc", "-y", "-r", "--no-progress", `--source=\\fs\choco`, "--params", "/NoDesktop"}; !reflect.DeepEqual(got, want) {
		t.Errorf("upgrade: got %q, want %q", got, want)
	}
	if got, want := UninstallArgs(e), []string{"uninstall", "vlc", "-y", "-r"}; !reflect.DeepEqual(got, want) {
		t.Errorf("uninstall: got %q, want %q", got, want)
	}
}

func TestChocoAvailableOnMissingPath(t *testing.T) {
	c := &Choco{Path: "/nonexistent/choco"}
	if ok, why := c.Available(); ok || why == "" {
		t.Errorf("expected unavailable with a reason, got %v %q", ok, why)
	}
}
