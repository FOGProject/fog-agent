package software

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// Real output from Fedora 44; the separators are real tabs.
const flatpakListOut = "com.spotify.Client\t1.2.95.453.g0eeebbed\nrest.insomnia.Insomnia\t13.3.0\n"

func TestParseFlatpakList(t *testing.T) {
	want := map[string]string{"com.spotify.client": "1.2.95.453.g0eeebbed", "rest.insomnia.insomnia": "13.3.0"}
	if got := ParseFlatpakList(flatpakListOut); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlatpakListEdges(t *testing.T) {
	// An empty version, a bare id, blank lines, and chatter.
	out := "org.gnome.Calculator\t\n\nOrg.Example.App\nNothing installed\nno-dot\n"
	want := map[string]string{"org.gnome.calculator": "", "org.example.app": ""}
	if got := ParseFlatpakList(out); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFlatpakArgs(t *testing.T) {
	e := Entry{Package: "org.gnome.Calculator", Source: "myremote", Args: `--user-arg "a b"`}
	if got, want := FlatpakInstallArgs(e), []string{"install", "--system", "-y", "--noninteractive", "myremote", "org.gnome.Calculator", "--user-arg", "a b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("install: got %q, want %q", got, want)
	}
	e.Source = ""
	if got, want := FlatpakInstallArgs(e), []string{"install", "--system", "-y", "--noninteractive", "flathub", "org.gnome.Calculator", "--user-arg", "a b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("install default source: got %q, want %q", got, want)
	}
	if got, want := FlatpakUpgradeArgs(e), []string{"update", "--system", "-y", "--noninteractive", "org.gnome.Calculator", "--user-arg", "a b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("upgrade: got %q, want %q", got, want)
	}
	if got, want := FlatpakUninstallArgs(e), []string{"uninstall", "--system", "-y", "--noninteractive", "org.gnome.Calculator"}; !reflect.DeepEqual(got, want) {
		t.Errorf("uninstall: got %q, want %q", got, want)
	}
}

// A pinned Install is refused before anything runs: the path does not
// exist, so a run would say something else.
func TestFlatpakRefusesPin(t *testing.T) {
	f := &Flatpak{Path: "/nonexistent/flatpak"}
	r := f.Install(context.Background(), Entry{Package: "org.gnome.Calculator", Version: "1.2"})
	want := "flatpak cannot hold an exact version (1.2); set the version policy to any or latest"
	if r.Status != StatusCannotRun || r.Details != want {
		t.Errorf("got %s %q, want cannot_run %q", r.Status, r.Details, want)
	}
}

func TestFlatpakAvailableOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the refusal is for other systems")
	}
	if ok, why := (&Flatpak{}).Available(); ok || why != "flatpak runs only on Linux" {
		t.Errorf("got %v %q", ok, why)
	}
}

func TestFlatpakAvailableOnMissingPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("flatpak is Linux only")
	}
	if ok, why := (&Flatpak{Path: "/nonexistent/flatpak"}).Available(); ok || why == "" {
		t.Errorf("expected unavailable with a reason, got %v %q", ok, why)
	}
}

// shimFlatpak writes an sh script that acts as flatpak over a list file,
// and returns its path.
func shimFlatpak(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "installed")
	if err := os.WriteFile(state, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
state="` + state + `"
cmd="$1"
case "$cmd" in
list) cat "$state" ;;
install) shift; shift; shift; shift; shift; printf '%s\t1.0\n' "$1" >> "$state" ;;
uninstall) grep -v "^$5	" "$state" > "$state.new"; mv "$state.new" "$state" ;;
update) exit 0 ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`
	p := filepath.Join(dir, "flatpak")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Install then list, end to end through Converge and a shim.
func TestFlatpakConvergeWithShim(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the shim is an sh script")
	}
	f := &Flatpak{Path: shimFlatpak(t)}
	e := Entry{ID: 1, Backend: "flatpak", Package: "org.gnome.Calculator", State: StatePresent}
	reports := Converge(context.Background(), f, []Entry{e})
	if r := reports[0]; r.Status != StatusInstalled || r.InstalledVersion != "1.0" {
		t.Fatalf("install: got %s %q (%s)", r.Status, r.InstalledVersion, r.Details)
	}
	if r := Converge(context.Background(), f, []Entry{e})[0]; r.Status != StatusConverged {
		t.Errorf("second run: got %s (%s)", r.Status, r.Details)
	}
	e.State = StateAbsent
	if r := Converge(context.Background(), f, []Entry{e})[0]; r.Status != StatusRemoved || r.InstalledVersion != "" {
		t.Errorf("remove: got %s %q (%s)", r.Status, r.InstalledVersion, r.Details)
	}
}

func TestBackendFor(t *testing.T) {
	for name, want := range map[string]string{"choco": "*software.Choco", "flatpak": "*software.Flatpak", "snap": "*software.Snap"} {
		b := BackendFor(name)
		if b == nil || reflect.TypeOf(b).String() != want {
			t.Errorf("BackendFor(%q) = %T, want %s", name, b, want)
		}
	}
	if b := BackendFor("apt"); b != nil {
		t.Errorf("BackendFor(apt) = %T, want nil", b)
	}
}

func TestGroup(t *testing.T) {
	entries := []Entry{
		{ID: 1, Backend: "snap"}, {ID: 2, Backend: "choco"}, {ID: 3, Backend: "snap"}, {ID: 4, Backend: "flatpak"},
	}
	names, groups := Group(entries)
	if want := []string{"snap", "choco", "flatpak"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %q, want %q", names, want)
	}
	if len(groups["snap"]) != 2 || groups["snap"][0].ID != 1 || groups["snap"][1].ID != 3 {
		t.Errorf("snap group = %+v", groups["snap"])
	}
	if names, groups := Group(nil); len(names) != 0 || len(groups) != 0 {
		t.Errorf("empty input gave %q %v", names, groups)
	}
}
