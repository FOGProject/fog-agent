package software

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// Shape of /var/lib/snapd/seed/seed.yaml on Ubuntu 24.04 Desktop: the dash
// sits on its own line and the keys are indented under it.
const ubuntuSeed = `snaps:
  -
    name: snapd
    channel: latest/stable/ubuntu-24.04
    file: snapd_23258.snap
  -
    name: core22
    channel: latest/stable
    file: core22_1722.snap
  -
    name: firefox
    channel: latest/stable/ubuntu-24.04
    file: firefox_4336.snap
  -
    name: gnome-42-2204
    channel: latest/stable/ubuntu-24.04
    file: gnome-42-2204_192.snap
  -
    name: snap-store
    channel: latest/stable/ubuntu-24.04
    file: snap-store_1216.snap
  -
    name: firmware-updater
    channel: latest/stable/ubuntu-24.04
    file: firmware-updater_147.snap
`

func TestParseSnapSeed(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"ubuntu 24.04", ubuntuSeed, []string{"snapd", "core22", "firefox", "gnome-42-2204", "snap-store", "firmware-updater"}, false},
		{"dash with first key", "snaps:\n  - name: Core22\n    channel: latest/stable\n    file: core22.snap\n  - name: \"firefox\"\n    file: f.snap\n", []string{"core22", "firefox"}, false},
		{"dash at column zero, other keys, nested list", "# seed\nsnaps:\n- name: a\n  assumes:\n  - x\n  - y\n  file: a.snap\n- name: b\nother:\n  - name: not-a-snap\n", []string{"a", "b"}, false},
		{"no snaps key", "version: 1\n", nil, false},
		{"empty", "", nil, false},
		{"item with no name", "snaps:\n  -\n    channel: latest/stable\n    file: x.snap\n", nil, true},
		{"item line is not a key", "snaps:\n  - name core22\n", nil, true},
		{"inline list", "snaps: [core22]\n", nil, true},
		{"tab indent", "snaps:\n\t- name: core22\n", nil, true},
		{"empty name", "snaps:\n  - name:\n    file: x\n", nil, true},
		{"line outside an item", "snaps:\n    name: core22\n", nil, true},
	}
	for _, c := range cases {
		got, err := ParseSnapSeed([]byte(c.in))
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if !c.wantErr && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// fakeSnap writes a `snap` script that lists firefox (seeded) and handmade
// (not seeded), and logs every other call to the returned file.
func fakeSnap(t *testing.T) (path, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in for snap")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = list ]; then\n" +
		"printf 'Name Version Rev Tracking Publisher Notes\\n'\n" +
		"printf 'firefox 1 1 latest/stable mozilla -\\n'\n" +
		"printf 'handmade 1 1 latest/stable someone -\\n'\n" +
		"exit 0\n" +
		"fi\n" +
		"echo \"$@\" >> " + log + "\n"
	path = filepath.Join(dir, "snap")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, log
}

func TestSnapKeepSeed(t *testing.T) {
	path, log := fakeSnap(t)
	seed := filepath.Join(t.TempDir(), "seed.yaml")
	if err := os.WriteFile(seed, []byte(ubuntuSeed), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Snap{Path: path, SeedPath: seed}
	installed, err := s.Installed(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	keep, _, err := s.Keep(t.Context(), nil, installed)
	if err != nil {
		t.Fatal(err)
	}
	if !keep["firefox"] || keep["handmade"] {
		t.Errorf("keep = %v, want firefox kept and handmade not", keep)
	}
	// Through the real removal step: only the hand-installed snap goes.
	got := ConvergeExclusive(t.Context(), "snap", s, nil, nil, Policy{Manage: ManageExclusive}, quiet)
	if !reflect.DeepEqual(got, []string{"handmade"}) {
		t.Errorf("removed %v, want [handmade]", got)
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "firefox") {
		t.Errorf("firefox was removed: %s", b)
	}
}

func TestSnapKeepMissingSeedIsNotAnError(t *testing.T) {
	path, _ := fakeSnap(t)
	s := &Snap{Path: path, SeedPath: filepath.Join(t.TempDir(), "absent.yaml")}
	keep, _, err := s.Keep(t.Context(), nil, map[string]string{"firefox": "1"})
	if err != nil {
		t.Fatalf("a missing seed must not be an error: %v", err)
	}
	if keep["firefox"] {
		t.Errorf("nothing is seeded, but firefox is kept: %v", keep)
	}
}

func TestSnapKeepUnreadableOrBadSeedFailsClosed(t *testing.T) {
	path, log := fakeSnap(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("snaps:\n  - name core22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory in place of the file cannot be read, even by root.
	unreadable := filepath.Join(dir, "unreadable")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, seed := range map[string]string{"unparsable": bad, "unreadable": unreadable} {
		s := &Snap{Path: path, SeedPath: seed}
		if _, _, err := s.Keep(t.Context(), nil, nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
		got := ConvergeExclusive(t.Context(), "snap", s, nil, nil, Policy{Manage: ManageExclusive}, quiet)
		if got != nil {
			t.Errorf("%s: removed %v", name, got)
		}
		if b, _ := os.ReadFile(log); len(b) != 0 {
			t.Errorf("%s: snap was called to remove: %s", name, b)
		}
	}
}

func TestParseFlatpakPreinstall(t *testing.T) {
	in := "# shipped apps\n[Flatpak Preinstall org.mozilla.firefox]\nBranch=stable\nIsRuntime=false\n\n" +
		"  [Flatpak Preinstall org.gnome.Calculator]  \nCollectionID=x\n[Other Group]\nname=y\n[Flatpak Preinstall ]\n"
	want := []string{"org.mozilla.firefox", "org.gnome.calculator"}
	if got := ParseFlatpakPreinstall([]byte(in)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := ParseFlatpakPreinstall(nil); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

func TestFlatpakKeepPreinstall(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "os.preinstall"), []byte("[Flatpak Preinstall org.Mozilla.Firefox]\nBranch=stable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "site.preinstall"), []byte("[Flatpak Preinstall org.site.Tool]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file without the suffix is not a preinstall file.
	if err := os.WriteFile(filepath.Join(b, "notes.txt"), []byte("[Flatpak Preinstall org.ignored.App]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &Flatpak{PreinstallDirs: []string{a, filepath.Join(t.TempDir(), "missing"), b}}
	keep, _, err := f.Keep(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"org.mozilla.firefox": true, "org.site.tool": true}
	if !reflect.DeepEqual(keep, want) {
		t.Errorf("got %v, want %v", keep, want)
	}
}

// A signature drift in either Keep fails the build here.
var (
	_ Keeper = (*Flatpak)(nil)
	_ Keeper = (*Snap)(nil)
)

// End to end through the real Flatpak and an sh shim: the preinstalled app
// stays, only the stray app is uninstalled.
func TestFlatpakExclusiveKeepsPreinstalled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the shim is an sh script")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"list)\n" +
		"case \"$*\" in\n" +
		"*columns=application,version*) printf 'org.Mozilla.Firefox\\t1\\norg.stray.App\\t1\\n' ;;\n" +
		"*) printf 'org.Mozilla.Firefox\\norg.stray.App\\n' ;;\n" +
		"esac ;;\n" +
		"*) echo \"$@\" >> " + log + " ;;\n" +
		"esac\n"
	shim := filepath.Join(dir, "flatpak")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pre := t.TempDir()
	if err := os.WriteFile(filepath.Join(pre, "os.preinstall"), []byte("[Flatpak Preinstall org.mozilla.firefox]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &Flatpak{Path: shim, PreinstallDirs: []string{pre}}
	got := ConvergeExclusive(t.Context(), "flatpak", f, nil, nil, Policy{Manage: ManageExclusive}, quiet)
	if !reflect.DeepEqual(got, []string{"org.stray.app"}) {
		t.Errorf("removed %v, want [org.stray.app]", got)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "uninstall --system -y --noninteractive org.stray.App") {
		t.Errorf("stray app was not uninstalled: %q", b)
	}
	if strings.Contains(strings.ToLower(string(b)), "firefox") {
		t.Errorf("preinstalled app was uninstalled: %q", b)
	}
}

func TestFlatpakKeepUnreadableFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	// A directory named like a keyfile cannot be read as a file, even by root.
	if err := os.Mkdir(filepath.Join(dir, "bad.preinstall"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &Flatpak{PreinstallDirs: []string{dir}}
	if _, _, err := f.Keep(t.Context(), nil, nil); err == nil {
		t.Fatal("want an error")
	}
}
