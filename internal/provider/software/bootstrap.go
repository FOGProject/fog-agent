package software

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/FOGProject/fog-agent/internal/procs"
)

// Bootstrap is the server's policy for a host that has no Chocolatey: an
// install script to fetch and run (design 0003 section 8, built after
// the Windows proof). Empty URL means do nothing, which is the default:
// an admin opts in to running a downloaded script as SYSTEM.
type Bootstrap struct {
	// URL of Chocolatey's install.ps1: the community one, or a copy on a
	// server the admin controls.
	URL string `json:"url"`
	// NupkgURL, when set, is where that script takes the chocolatey
	// package from (its chocolateyDownloadUrl), for hosts with no route
	// to the community feed.
	NupkgURL string `json:"nupkg_url"`
}

// BootstrapTimeout bounds the whole bootstrap: fetch, then the script,
// which downloads and unpacks the package.
const BootstrapTimeout = 15 * time.Minute

// bootstrapMaxScript is more than install.ps1 has ever been.
const bootstrapMaxScript = 1 << 20

// BootstrapClient fetches over TLS trusting the system roots plus the
// FOG CA, so the script can come from the community site or from the
// FOG server itself.
func BootstrapClient(caPEM []byte) *http.Client {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM(caPEM)
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

// BootstrapArgs is the PowerShell command line for the fetched script.
func BootstrapArgs(script string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script}
}

// BootstrapEnv is the script's environment: the process's own plus the
// package URL when the policy names one.
func BootstrapEnv(b Bootstrap) []string {
	env := os.Environ()
	if b.NupkgURL != "" {
		env = append(env, "chocolateyDownloadUrl="+b.NupkgURL)
	}
	return env
}

// InstallChoco fetches the script into dir and runs it. The returned
// text is the script's output tail, with or without an error.
func InstallChoco(ctx context.Context, httpc *http.Client, b Bootstrap, dir string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", errors.New("the Chocolatey bootstrap is a PowerShell script; nothing to run on " + runtime.GOOS)
	}
	script, err := FetchScript(ctx, httpc, b.URL)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "choco-install.ps1")
	if err := os.WriteFile(path, script, 0o600); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", BootstrapArgs(path)...)
	cmd.Env = BootstrapEnv(b)
	procs.Attach(cmd)
	out := procs.NewTail(MaxDetails)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("timed out after %s", BootstrapTimeout)
	}
	return out.String(), err
}

// FetchScript downloads the install script, refusing anything but a
// 200 and anything implausibly large.
func FetchScript(ctx context.Context, httpc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, bootstrapMaxScript+1))
	if err != nil {
		return nil, err
	}
	if len(b) > bootstrapMaxScript {
		return nil, fmt.Errorf("fetch %s: larger than %d bytes", url, bootstrapMaxScript)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("fetch %s: empty", url)
	}
	return b, nil
}
