//go:build windows

package activation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/FOGProject/fog-agent/internal/procs"
)

// callTimeout bounds one PowerShell call. Activate() goes to Microsoft or
// to a KMS host, and a network that drops the packets must not hold the
// reconcile loop.
const callTimeout = 2 * time.Minute

// product selects Windows itself among the licensed products (Office
// registers its own), the same ApplicationID slmgr.vbs filters on.
const product = `Get-CimInstance -ClassName SoftwareLicensingProduct -Filter "ApplicationID='55c92734-d682-4d71-983e-d6ec3f16059f' AND PartialProductKey IS NOT NULL" | Select-Object -First 1`

// wrap runs body and turns any failure into one stderr line: the Software
// Licensing error code when WMI carries one (0xC004F050 and friends, the
// codes slmgr prints), else the exception text.
func wrap(body string) string {
	return `$ErrorActionPreference = 'Stop'
try {
` + body + `
} catch {
  $e = $_.Exception
  $c = $null
  if ($e.ErrorData) { $c = $e.ErrorData.CimInstanceProperties['error_Code'].Value }
  if ($c) { [Console]::Error.WriteLine(('0x{0:X8}' -f [uint32]$c)) } else { [Console]::Error.WriteLine($e.Message) }
  exit 1
}`
}

var (
	queryScript = wrap(`$p = ` + product + `
if ($p) { '{0} {1}' -f $p.PartialProductKey, $p.LicenseStatus } else { '- 0' }`)

	// The key is read from stdin. It is never on the command line, where
	// every process on the machine could read it (design 0016 §3).
	installScript = wrap(`$k = [Console]::In.ReadLine()
$s = Get-CimInstance -ClassName SoftwareLicensingService
Invoke-CimMethod -InputObject $s -MethodName InstallProductKey -Arguments @{ProductKey = $k} | Out-Null
Invoke-CimMethod -InputObject $s -MethodName RefreshLicenseStatus | Out-Null`)

	activateScript = wrap(`$p = ` + product + `
if (-not $p) { throw 'no Windows product holds a key' }
Invoke-CimMethod -InputObject $p -MethodName Activate | Out-Null`)
)

// powershell runs script with stdin, and returns stdout, or stderr's first
// line as the error.
func powershell(ctx context.Context, script, stdin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	exe := filepath.Join(sys, "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	procs.Attach(cmd)
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("timed out after %s", callTimeout)
		}
		if line, _, _ := strings.Cut(strings.TrimSpace(errOut.String()), "\n"); line != "" {
			return "", errors.New(procs.Clip(strings.TrimSpace(line), 200))
		}
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func osQuery(ctx context.Context) (License, error) {
	out, err := powershell(ctx, queryScript, "")
	if err != nil {
		return License{}, err
	}
	partial, status, ok := strings.Cut(out, " ")
	n, convErr := strconv.Atoi(status)
	if !ok || convErr != nil {
		return License{}, fmt.Errorf("unexpected answer %q", procs.Clip(out, 80))
	}
	if partial == "-" {
		partial = ""
	}
	// LicenseStatus 1 is Licensed; every other value is some kind of not.
	return License{Partial: partial, Licensed: n == 1}, nil
}

func osInstall(ctx context.Context, key string) error {
	_, err := powershell(ctx, installScript, key+"\n")
	return err
}

func osActivate(ctx context.Context) error {
	_, err := powershell(ctx, activateScript, "")
	return err
}
