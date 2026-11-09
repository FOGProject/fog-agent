package software

import (
	"context"
	"reflect"
	"runtime"
	"testing"
)

// Real output from Fedora 44.
const snapListOut = "Name               Version          Rev    Tracking       Publisher      Notes\n" +
	"bare               1.0              5      latest/stable  canonical**    base\n" +
	"core18             20250910         2952   latest/stable  canonical**    base\n" +
	"core22             20250822         2133   latest/stable  canonical**    base\n" +
	"discord            1.0.152          302    latest/stable  snapcrafters*  -\n"

func TestParseSnapList(t *testing.T) {
	want := map[string]string{"bare": "1.0", "core18": "20250910", "core22": "20250822", "discord": "1.0.152"}
	if got := ParseSnapList(snapListOut); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseSnapListEmpty(t *testing.T) {
	for _, out := range []string{"", "No snaps are installed yet. Try 'snap install hello-world'.\n"} {
		if got := ParseSnapList(out); len(got) != 0 {
			t.Errorf("%q: got %v, want none", out, got)
		}
	}
}

func TestSnapArgs(t *testing.T) {
	e := Entry{Package: "discord", Source: "latest/edge", Args: `--classic --foo "a b"`}
	if got, want := SnapInstallArgs(e), []string{"install", "discord", "--channel=latest/edge", "--classic", "--foo", "a b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("install: got %q, want %q", got, want)
	}
	if got, want := SnapUpgradeArgs(e), []string{"refresh", "discord", "--channel=latest/edge", "--classic", "--foo", "a b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("upgrade: got %q, want %q", got, want)
	}
	e.Source, e.Args = "", ""
	if got, want := SnapInstallArgs(e), []string{"install", "discord"}; !reflect.DeepEqual(got, want) {
		t.Errorf("install, no source: got %q, want %q", got, want)
	}
	if got, want := SnapUpgradeArgs(e), []string{"refresh", "discord"}; !reflect.DeepEqual(got, want) {
		t.Errorf("upgrade, no source: got %q, want %q", got, want)
	}
	if got, want := SnapUninstallArgs(e), []string{"remove", "discord"}; !reflect.DeepEqual(got, want) {
		t.Errorf("uninstall: got %q, want %q", got, want)
	}
}

// A pinned Install is refused before anything runs: the path does not
// exist, so a run would say something else.
func TestSnapRefusesPin(t *testing.T) {
	s := &Snap{Path: "/nonexistent/snap"}
	r := s.Install(context.Background(), Entry{Package: "discord", Version: "1.0.152"})
	want := "snap cannot hold an exact version (1.0.152); set the version policy to any or latest"
	if r.Status != StatusCannotRun || r.Details != want {
		t.Errorf("got %s %q, want cannot_run %q", r.Status, r.Details, want)
	}
}

func TestSnapAvailableOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the refusal is for other systems")
	}
	if ok, why := (&Snap{}).Available(); ok || why != "snap runs only on Linux" {
		t.Errorf("got %v %q", ok, why)
	}
}

func TestSnapAvailableOnMissingPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("snap is Linux only")
	}
	if ok, why := (&Snap{Path: "/nonexistent/snap"}).Available(); ok || why == "" {
		t.Errorf("expected unavailable with a reason, got %v %q", ok, why)
	}
}
