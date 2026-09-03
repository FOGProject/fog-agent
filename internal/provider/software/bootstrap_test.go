package software

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestFetchScript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/install.ps1":
			w.Write([]byte("Write-Host hi\n"))
		case "/empty":
		case "/big":
			w.Write([]byte(strings.Repeat("x", bootstrapMaxScript+1)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if b, err := FetchScript(ctx, srv.Client(), srv.URL+"/install.ps1"); err != nil || string(b) != "Write-Host hi\n" {
		t.Errorf("good fetch: %q %v", b, err)
	}
	for _, p := range []string{"/missing", "/empty", "/big"} {
		if _, err := FetchScript(ctx, srv.Client(), srv.URL+p); err == nil {
			t.Errorf("%s: expected an error", p)
		}
	}
}

func TestBootstrapArgsAndEnv(t *testing.T) {
	if got, want := BootstrapArgs(`C:\x\i.ps1`), []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", `C:\x\i.ps1`}; !reflect.DeepEqual(got, want) {
		t.Errorf("args %q", got)
	}
	has := func(env []string, kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}
	if has(BootstrapEnv(Bootstrap{}), "chocolateyDownloadUrl=") {
		t.Error("no package URL must set nothing")
	}
	if !has(BootstrapEnv(Bootstrap{NupkgURL: "https://fog/choco.nupkg"}), "chocolateyDownloadUrl=https://fog/choco.nupkg") {
		t.Error("package URL must be passed as chocolateyDownloadUrl")
	}
}

func TestInstallChocoRefusesOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	if _, err := InstallChoco(context.Background(), http.DefaultClient, Bootstrap{URL: "https://example.invalid/x"}, t.TempDir()); err == nil || !strings.Contains(err.Error(), runtime.GOOS) {
		t.Errorf("expected a refusal naming the OS, got %v", err)
	}
}
