package software

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnwanted(t *testing.T) {
	installed := map[string]string{"vlc": "3", "7zip": "22", "oldtool": "1", "chocolatey": "2", "": "x", "  ": "y"}
	entries := []Entry{
		{Package: "VLC", State: StatePresent},    // assigned present, any case
		{Package: "OldTool", State: StateAbsent}, // an absent entry still manages its package
	}
	keep := map[string]bool{"chocolatey": true}
	cases := []struct {
		name   string
		manage string
		want   []string
	}{
		{"exclusive", ManageExclusive, []string{"7zip"}},
		{"assigned", ManageAssigned, nil},
		{"empty", "", nil},
		{"unknown", "everything", nil},
	}
	for _, c := range cases {
		if got := Unwanted(c.manage, entries, installed, keep); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// With nothing kept and nothing assigned, every named package is
	// unwanted and the blank ids still are not.
	if got, want := Unwanted(ManageExclusive, nil, installed, nil), []string{"7zip", "chocolatey", "oldtool", "vlc"}; !reflect.DeepEqual(got, want) {
		t.Errorf("no entries: got %v, want %v", got, want)
	}
}

// keeperFake is the fake backend with a Keeper.
type keeperFake struct {
	fake
	keep    map[string]bool
	order   []string
	keepErr error
}

func (k *keeperFake) Keep(context.Context, []Entry, map[string]string) (map[string]bool, []string, error) {
	return k.keep, k.order, k.keepErr
}

func newKeeperFake() *keeperFake {
	return &keeperFake{
		fake: fake{available: true, installed: map[string]string{"vlc": "3", "7zip": "22", "gimp": "2", "runtime": "1"}},
		keep: map[string]bool{"runtime": true},
	}
}

func uninstalls(f *fake) []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "uninstall:") {
			out = append(out, strings.TrimPrefix(c, "uninstall:"))
		}
	}
	return out
}

func quiet(string) {}

// exclusiveP runs the removal step with the given reports and policy.
func exclusiveP(name string, b Backend, entries []Entry, reports []Report, p Policy, logf func(string)) []string {
	return ConvergeExclusive(context.Background(), name, b, entries, reports, p, logf)
}

// exclusive is exclusiveP with no reports and no retain list.
func exclusive(name string, b Backend, entries []Entry, manage string, logf func(string)) []string {
	return exclusiveP(name, b, entries, nil, Policy{Manage: manage}, logf)
}

func TestConvergeExclusiveSkipsWhenAPresentEntryIsMissing(t *testing.T) {
	k := newKeeperFake()
	k.exit = map[string]int{"broken": 1603} // the install fails and leaves a leftover dependency behind
	k.installed["leftover"] = "1"
	entries := []Entry{{Package: "vlc", State: StatePresent}, {Package: "broken", State: StatePresent}}
	reports := Converge(context.Background(), k, entries)
	var logged []string
	got := exclusiveP("choco", k, entries, reports, Policy{Manage: ManageExclusive}, func(s string) { logged = append(logged, s) })
	if got != nil || len(uninstalls(&k.fake)) != 0 {
		t.Errorf("removed %v although an assigned package is not installed", uninstalls(&k.fake))
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "broken") {
		t.Errorf("the reason must be logged, got %q", logged)
	}
	// An absent entry that is gone is the goal, not a reason to skip.
	k = newKeeperFake()
	entries = []Entry{{Package: "vlc", State: StatePresent}, {Package: "ghost", State: StateAbsent}}
	if got := exclusiveP("choco", k, entries, nil, Policy{Manage: ManageExclusive}, quiet); !reflect.DeepEqual(got, []string{"7zip", "gimp"}) {
		t.Errorf("got %v", got)
	}
}

func TestConvergeExclusiveSkipsOnCannotRunReport(t *testing.T) {
	k := newKeeperFake()
	reports := []Report{{Entry: Entry{Package: "vlc"}, Status: StatusCannotRun}}
	got := exclusiveP("choco", k, []Entry{{Package: "vlc", State: StatePresent}}, reports, Policy{Manage: ManageExclusive}, quiet)
	if got != nil || len(uninstalls(&k.fake)) != 0 {
		t.Errorf("removed %v after a cannot_run report", got)
	}
}

func TestConvergeExclusiveRetainKeepsDisabledEntryPackages(t *testing.T) {
	k := newKeeperFake()
	p := Policy{Manage: ManageExclusive, Retain: []RetainItem{
		{Backend: "CHOCO", Package: "GIMP"},   // case-insensitive on both
		{Backend: "snap", Package: "7zip"},    // another backend's retain item must not protect this one
		{Backend: "choco", Package: "absent"}, // not installed: fine
	}}
	got := exclusiveP("choco", k, []Entry{{Package: "vlc", State: StatePresent}}, nil, p, quiet)
	if want := []string{"7zip"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A retained package keeps its dependency closure, like an assigned one.
func TestChocoRetainFollowsDependencies(t *testing.T) {
	lib := t.TempDir()
	writeNuspec(t, lib, "chocolatey")
	writeNuspec(t, lib, "kept", "dep")
	writeNuspec(t, lib, "dep")
	writeNuspec(t, lib, "stray")
	f := &chocoFake{keeperFake: *newKeeperFake(), c: &Choco{libDir: lib}}
	f.installed = map[string]string{"chocolatey": "2", "kept": "1", "dep": "1", "stray": "1"}
	p := Policy{Manage: ManageExclusive, Retain: []RetainItem{{Backend: "choco", Package: "kept"}}}
	got := exclusiveP("choco", f, nil, nil, p, quiet)
	if want := []string{"stray"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBackendNamesResolve(t *testing.T) {
	names := BackendNames()
	if len(names) == 0 {
		t.Fatal("no backends")
	}
	for _, n := range names {
		if BackendFor(n) == nil {
			t.Errorf("BackendFor(%q) is nil", n)
		}
	}
}

func TestConvergeExclusiveRemovesOnlyUnwanted(t *testing.T) {
	k := newKeeperFake()
	entries := []Entry{{Package: "vlc", State: StatePresent}}
	got := exclusive("choco", k, entries, ManageExclusive, quiet)
	want := []string{"7zip", "gimp"}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(uninstalls(&k.fake), want) {
		t.Fatalf("removed %v, uninstalled %v, want %v", got, uninstalls(&k.fake), want)
	}
	if _, ok := k.installed["vlc"]; !ok {
		t.Error("an assigned package was removed")
	}
	if _, ok := k.installed["runtime"]; !ok {
		t.Error("a kept package was removed")
	}
}

func TestConvergeExclusiveKeepsRemovalOrder(t *testing.T) {
	k := newKeeperFake()
	k.order = []string{"gimp", "7zip", "notinstalled"}
	got := exclusive("choco", k, nil, ManageExclusive, quiet)
	// vlc is unwanted but absent from order, so it goes last.
	if want := []string{"gimp", "7zip", "vlc"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestConvergeExclusiveKeepErrorRemovesNothing(t *testing.T) {
	k := newKeeperFake()
	k.keepErr = errors.New("nuspec missing")
	var logged []string
	got := exclusive("choco", k, nil, ManageExclusive, func(s string) { logged = append(logged, s) })
	if got != nil || len(uninstalls(&k.fake)) != 0 {
		t.Errorf("removed %v after a Keep error", uninstalls(&k.fake))
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "nuspec missing") {
		t.Errorf("the reason must be logged, got %q", logged)
	}
}

func TestConvergeExclusiveNotExclusiveRemovesNothing(t *testing.T) {
	for _, manage := range []string{"", ManageAssigned, "bogus"} {
		k := newKeeperFake()
		exclusive("choco", k, nil, manage, quiet)
		if len(k.calls) != 0 {
			t.Errorf("manage %q: backend was called: %q", manage, k.calls)
		}
	}
}

func TestConvergeExclusiveUnavailableOrListErrorDoesNothing(t *testing.T) {
	k := newKeeperFake()
	k.available = false
	if got := exclusive("choco", k, nil, ManageExclusive, quiet); got != nil || len(k.calls) != 0 {
		t.Errorf("unavailable: removed %v, calls %q", got, k.calls)
	}
	k = newKeeperFake()
	k.listErr = errors.New("boom")
	if got := exclusive("choco", k, nil, ManageExclusive, quiet); got != nil || len(uninstalls(&k.fake)) != 0 {
		t.Errorf("list error: removed %v", got)
	}
}

// A backend with no Keeper keeps nothing beyond the assigned entries.
func TestConvergeExclusiveWithoutKeeper(t *testing.T) {
	f := &fake{available: true, installed: map[string]string{"vlc": "3", "gimp": "2"}}
	got := exclusive("flatpak", f, []Entry{{Package: "vlc"}}, ManageExclusive, quiet)
	if want := []string{"gimp"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// writeNuspec writes <lib>/<id>/<id>.nuspec depending on deps.
func writeNuspec(t *testing.T, lib, id string, deps ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><package xmlns="http://schemas.microsoft.com/packaging/2015/06/nuspec.xsd"><metadata><id>` + id + `</id><dependencies>`)
	for _, d := range deps {
		b.WriteString(`<dependency id="` + d + `" version="1.0" />`)
	}
	b.WriteString(`</dependencies></metadata></package>`)
	dir := filepath.Join(lib, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".nuspec"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChocoKeepClosureAndPlumbing(t *testing.T) {
	lib := t.TempDir()
	writeNuspec(t, lib, "chocolatey")
	writeNuspec(t, lib, "chocolatey-core.extension")
	writeNuspec(t, lib, "a", "b")
	writeNuspec(t, lib, "b", "c", "notinstalled")
	writeNuspec(t, lib, "c")
	writeNuspec(t, lib, "junk")
	installed := map[string]string{"chocolatey": "2", "chocolatey-core.extension": "1", "a": "1", "b": "1", "c": "1", "junk": "1", "chocolatey-extra": "1"}
	c := &Choco{libDir: lib}
	keep, _, err := c.Keep(context.Background(), []Entry{{Package: "A", State: StatePresent}}, installed)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"chocolatey", "chocolatey-core.extension", "a", "b", "c"} {
		if !keep[id] {
			t.Errorf("%s must be kept", id)
		}
	}
	for _, id := range []string{"junk", "chocolatey-extra"} {
		if keep[id] {
			t.Errorf("%s must not be kept", id)
		}
	}
	if got, want := Unwanted(ManageExclusive, []Entry{{Package: "a"}}, installed, keep), []string{"chocolatey-extra", "junk"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unwanted = %v, want %v", got, want)
	}
}

// A kept package's dependencies are kept too, not only the assigned ones.
func TestChocoKeepFollowsKeptPackagesDependencies(t *testing.T) {
	lib := t.TempDir()
	writeNuspec(t, lib, "chocolatey", "chocolatey-helper")
	writeNuspec(t, lib, "chocolatey-helper")
	keep, _, err := (&Choco{libDir: lib}).Keep(context.Background(), nil, map[string]string{"chocolatey": "2", "chocolatey-helper": "1"})
	if err != nil || !keep["chocolatey-helper"] {
		t.Errorf("keep = %v, err = %v", keep, err)
	}
}

// Chocolatey and its extensions are kept by name: no nuspec for them is not
// an error, and the assigned package's closure is still kept.
func TestChocoKeepPlumbingWithoutNuspec(t *testing.T) {
	lib := t.TempDir()
	writeNuspec(t, lib, "a", "b")
	writeNuspec(t, lib, "b")
	installed := map[string]string{"chocolatey": "2", "chocolatey-core.extension": "1", "a": "1", "b": "1", "stray": "1"}
	keep, _, err := (&Choco{libDir: lib}).Keep(context.Background(), []Entry{{Package: "a", State: StatePresent}}, installed)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"chocolatey", "chocolatey-core.extension", "a", "b"} {
		if !keep[id] {
			t.Errorf("%s must be kept", id)
		}
	}
	if keep["stray"] {
		t.Error("stray must not be kept")
	}
}

func TestChocoKeepMissingNuspecFailsClosed(t *testing.T) {
	lib := t.TempDir()
	writeNuspec(t, lib, "chocolatey")
	c := &Choco{libDir: lib}
	// a is installed and assigned but has no nuspec.
	_, _, err := c.Keep(context.Background(), []Entry{{Package: "a", State: StatePresent}}, map[string]string{"chocolatey": "2", "a": "1"})
	if err == nil {
		t.Fatal("a missing nuspec for an assigned package must be an error")
	}
	// An unparseable nuspec is the same.
	if err := os.MkdirAll(filepath.Join(lib, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "a", "a.nuspec"), []byte("<package><metadata>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Keep(context.Background(), []Entry{{Package: "a", State: StatePresent}}, map[string]string{"chocolatey": "2", "a": "1"}); err == nil {
		t.Error("a broken nuspec must be an error")
	}
	// And through Converge it removes nothing.
	f := &chocoFake{keeperFake: *newKeeperFake(), c: c}
	f.installed = map[string]string{"chocolatey": "2", "a": "1", "stray": "1"}
	got := exclusive("choco", f, []Entry{{Package: "a", State: StatePresent}}, ManageExclusive, quiet)
	if got != nil || len(uninstalls(&f.fake)) != 0 {
		t.Errorf("removed %v despite the nuspec error", got)
	}
}

// chocoFake is the fake backend with the real Choco.Keep.
type chocoFake struct {
	keeperFake
	c *Choco
}

func (f *chocoFake) Keep(ctx context.Context, a []Entry, i map[string]string) (map[string]bool, []string, error) {
	return f.c.Keep(ctx, a, i)
}

func TestChocoKeepOrdersDependentsFirst(t *testing.T) {
	lib := t.TempDir()
	writeNuspec(t, lib, "chocolatey")
	writeNuspec(t, lib, "app", "lib1")  // app needs lib1
	writeNuspec(t, lib, "lib1", "lib2") // lib1 needs lib2
	writeNuspec(t, lib, "lib2")
	writeNuspec(t, lib, "another")
	installed := map[string]string{"chocolatey": "2", "lib2": "1", "lib1": "1", "app": "1", "another": "1"}
	_, order, err := (&Choco{libDir: lib}).Keep(context.Background(), nil, installed)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if len(order) != 4 || pos["app"] > pos["lib1"] || pos["lib1"] > pos["lib2"] {
		t.Errorf("order = %v, want app before lib1 before lib2", order)
	}
}

// An unreadable nuspec among the unwanted only loses the ordering.
func TestChocoKeepOrderFallsBackWhenAnUnwantedNuspecIsMissing(t *testing.T) {
	lib := t.TempDir()
	writeNuspec(t, lib, "chocolatey")
	keep, order, err := (&Choco{libDir: lib}).Keep(context.Background(), nil, map[string]string{"chocolatey": "2", "stray": "1"})
	if err != nil || order != nil || !keep["chocolatey"] {
		t.Errorf("keep = %v, order = %v, err = %v", keep, order, err)
	}
}

func TestChocoLibDir(t *testing.T) {
	t.Setenv("ChocolateyInstall", filepath.Join("x", "choco"))
	if got, want := (&Choco{}).lib(), filepath.Join("x", "choco", "lib"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := (&Choco{libDir: "y"}).lib(); got != "y" {
		t.Errorf("got %q", got)
	}
}

// Real `snap list` output: Notes is the sixth column.
const snapListNotes = "Name               Version          Rev    Tracking       Publisher      Notes\n" +
	"bare               1.0              5      latest/stable  canonical**    base\n" +
	"core22             20250822         2133   latest/stable  canonical**    base\n" +
	"firefox            131.0            4336   latest/stable  mozilla**      -\n" +
	"gnome-42-2204      0+git.ff35a85    176    latest/stable  canonical**    -\n" +
	"pc-kernel          6.8.0-45.45.1    1234   22/stable      canonical**    kernel,disabled\n" +
	"pc                 22-0.4           145    22/stable      canonical**    gadget\n" +
	"snapd              2.65             21759  latest/stable  canonical**    snapd\n" +
	"core               16-2.61          17200  latest/stable  canonical**    core\n" +
	"discord            1.0.152          302    latest/stable  snapcrafters*  classic\n"

func TestParseSnapPlumbing(t *testing.T) {
	want := map[string]bool{"bare": true, "core22": true, "pc-kernel": true, "pc": true, "snapd": true, "core": true}
	if got := ParseSnapPlumbing(snapListNotes); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := ParseSnapPlumbing(""); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

const snapConnectionsOut = "Interface               Plug                                Slot                              Notes\n" +
	"content[gnome-42-2204]  firefox:gnome-42-2204               gnome-42-2204:gnome-42-2204       -\n" +
	"desktop                 firefox:desktop                     :desktop                          -\n" +
	"network                 firefox:network                     :network                          -\n" +
	"audio-record            firefox:audio-record                -                                 -\n" +
	"content                 firefox:gtk-3-themes                gtk-common-themes:gtk-3-themes    -\n"

func TestParseSnapSlotProviders(t *testing.T) {
	want := map[string]bool{"gnome-42-2204": true, "gtk-common-themes": true}
	if got := ParseSnapSlotProviders(snapConnectionsOut); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := ParseSnapSlotProviders(""); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

func TestMatchFlatpakID(t *testing.T) {
	out := "org.mozilla.Firefox\norg.gnome.Calculator\n"
	if got := MatchFlatpakID(out, "org.mozilla.firefox"); got != "org.mozilla.Firefox" {
		t.Errorf("got %q", got)
	}
	if got := MatchFlatpakID(out, "org.other.App"); got != "org.other.App" {
		t.Errorf("an unknown id must pass through, got %q", got)
	}
}

func TestPolicyManageJSON(t *testing.T) {
	// manage is optional on the wire: an older server omits it.
	var p Policy
	if err := json.Unmarshal([]byte(`{"drift_interval":60,"entries":[]}`), &p); err != nil || p.Manage != "" {
		t.Errorf("missing manage: %+v, %v", p, err)
	}
	if err := json.Unmarshal([]byte(`{"manage":"exclusive"}`), &p); err != nil || p.Manage != ManageExclusive {
		t.Errorf("exclusive: %+v, %v", p, err)
	}
	if b, _ := json.Marshal(Policy{}); strings.Contains(string(b), "manage") {
		t.Errorf("empty manage must be omitted: %s", b)
	}
}
