package software

import (
	"context"
	"errors"
	"reflect"
	"testing"

	provider "github.com/FOGProject/fog-agent/internal/provider/software"
)

type fakeBackend struct {
	available bool
	installed map[string]string
	err       error
}

func (f fakeBackend) Available() (bool, string) { return f.available, "" }
func (f fakeBackend) Installed(context.Context) (map[string]string, error) {
	return f.installed, f.err
}
func (fakeBackend) Install(context.Context, provider.Entry) provider.Result { return provider.Result{} }
func (fakeBackend) Upgrade(context.Context, provider.Entry) provider.Result { return provider.Result{} }
func (fakeBackend) Uninstall(context.Context, provider.Entry) provider.Result {
	return provider.Result{}
}

func TestAppendBackend(t *testing.T) {
	base := []Program{{Name: "bash", Source: "dpkg"}}
	got, err := appendBackend(context.Background(), base, "snap", fakeBackend{available: true, installed: map[string]string{"discord": "1.0"}})
	want := []Program{{Name: "bash", Source: "dpkg"}, {Name: "discord", Version: "1.0", Source: "snap"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v, want %+v", got, err, want)
	}
	// Unavailable and missing backends contribute nothing, with no error.
	for name, b := range map[string]provider.Backend{
		"unavailable": fakeBackend{available: false, installed: map[string]string{"x": "1"}},
		"nil":         nil,
	} {
		if got, err := appendBackend(context.Background(), base, "snap", b); err != nil || !reflect.DeepEqual(got, base) {
			t.Errorf("%s: got %+v, %v", name, got, err)
		}
	}
	// An available backend whose list fails is an error, not a partial list.
	if got, err := appendBackend(context.Background(), base, "snap", fakeBackend{available: true, err: errors.New("boom")}); err == nil || got != nil {
		t.Errorf("list failure: got %+v, %v, want nil and an error", got, err)
	}
}

// One failing backend among several voids the whole report.
func TestAppendBackendsErrorVoidsThePartialList(t *testing.T) {
	backends := map[string]provider.Backend{
		"choco":   fakeBackend{available: true, installed: map[string]string{"git": "2"}},
		"flatpak": fakeBackend{available: true, err: errors.New("boom")},
		"snap":    fakeBackend{available: false},
	}
	lookup := func(n string) provider.Backend { return backends[n] }
	got, err := appendBackends(context.Background(), nil, []string{"choco", "flatpak", "snap"}, lookup)
	if err == nil || got != nil {
		t.Fatalf("got %+v, %v, want nil and an error", got, err)
	}
	// With the failure gone the same set reports, the unavailable one silent.
	backends["flatpak"] = fakeBackend{available: true, installed: map[string]string{"org.x.App": "1"}}
	got, err = appendBackends(context.Background(), nil, []string{"choco", "flatpak", "snap"}, lookup)
	if err != nil || len(got) != 2 {
		t.Errorf("got %+v, %v, want two rows", got, err)
	}
}
