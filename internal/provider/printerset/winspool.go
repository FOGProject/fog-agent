package printerset

import (
	"context"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
)

// Winspool drives the Windows print spooler through PowerShell's
// PrintManagement module.
//
// PowerShell here and direct API calls for the READ path (see
// internal/printers), which is a deliberate split rather than an
// inconsistency. Reading happens on every facts interval on every host, so
// it is worth the syscalls to avoid a PowerShell launch. Adding or removing
// a printer happens when an admin changes an assignment, which is rare -- and
// the Win32 equivalents (AddPrinter plus XcvData to create the port, with a
// hand-packed PORT_DATA_1 structure) are enough unsafe pointer arithmetic
// that getting them wrong costs more than the launch does.
type Winspool struct{}

// Available reports whether PowerShell is here. It is, on every Windows this
// agent supports, so a failure means something is badly wrong rather than
// merely unconfigured.
func (Winspool) Available() (bool, string) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		return false, "powershell.exe was not found; printers cannot be managed"
	}
	return true, ""
}

// Add installs a queue, creating its port first.
//
// Idempotent in both halves, which is what lets one call serve both an add
// and a reconfigure: the port is created only if it is missing, and an
// existing printer is re-pointed with Set-Printer rather than failing.
func (Winspool) Add(ctx context.Context, p Printer) Result {
	port, err := portFor(p.URI)
	if err != nil {
		return Result{Status: StatusFailed, Error: err.Error()}
	}
	script := `$ErrorActionPreference='Stop'
$name = ` + psQuote(p.Name) + `
$port = ` + psQuote(port.name) + `
if (-not (Get-PrinterPort -Name $port -ErrorAction SilentlyContinue)) {
  ` + port.create + `
}
$existing = Get-Printer -Name $name -ErrorAction SilentlyContinue
if ($existing) {
  Set-Printer -Name $name -PortName $port
} else {
  Add-Printer -Name $name -PortName $port -DriverName ` + psQuote(p.Driver) + `
}`
	if out, err := powershell(ctx, script); err != nil {
		return Result{Status: StatusFailed, Error: message(out, err)}
	}
	return Result{Status: StatusInstalled}
}

// Remove deletes a queue. The port is left behind on purpose: another queue
// may be using it, and a dangling port costs nothing.
func (Winspool) Remove(ctx context.Context, name string) Result {
	script := `$ErrorActionPreference='Stop'
Remove-Printer -Name ` + psQuote(name)
	if out, err := powershell(ctx, script); err != nil {
		return Result{Status: StatusFailed, Error: message(out, err)}
	}
	return Result{Status: StatusRemoved}
}

// SetDefault makes a queue the machine's default.
func (Winspool) SetDefault(ctx context.Context, name string) Result {
	script := `$ErrorActionPreference='Stop'
(Get-CimInstance -ClassName Win32_Printer -Filter ` +
		psQuote("Name='"+strings.ReplaceAll(name, "'", "''")+"'") +
		`) | Invoke-CimMethod -MethodName SetDefaultPrinter`
	if out, err := powershell(ctx, script); err != nil {
		return Result{Status: StatusFailed, Error: message(out, err)}
	}
	return Result{Status: StatusUpdated}
}

// winPort is a port to create: its name, and the PowerShell that makes it.
type winPort struct {
	name   string
	create string
}

// portFor turns a device URI into a Windows port.
//
// The reverse of the reconstruction internal/printers does when reporting,
// and the reason one printer row can serve both platforms: socket://host:9100
// is a Standard TCP/IP RAW port here and a socket device URI on CUPS, and
// they are the same printer.
func portFor(uri string) (winPort, error) {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || u.Scheme == "" {
		return winPort{}, errBadURI{uri}
	}
	switch strings.ToLower(u.Scheme) {
	case "socket":
		host := u.Hostname()
		if host == "" {
			return winPort{}, errBadURI{uri}
		}
		port := u.Port()
		if port == "" {
			port = "9100"
		}
		if _, err := strconv.Atoi(port); err != nil {
			return winPort{}, errBadURI{uri}
		}
		name := "IP_" + host + "_" + port
		return winPort{
			name: name,
			create: "Add-PrinterPort -Name " + psQuote(name) +
				" -PrinterHostAddress " + psQuote(host) +
				" -PortNumber " + port,
		}, nil
	case "lpd":
		host := u.Hostname()
		queue := strings.TrimPrefix(u.Path, "/")
		if host == "" || queue == "" {
			return winPort{}, errBadURI{uri}
		}
		name := "LPR_" + host + "_" + queue
		return winPort{
			name: name,
			create: "Add-PrinterPort -Name " + psQuote(name) +
				" -LprHostAddress " + psQuote(host) +
				" -LprQueueName " + psQuote(queue),
		}, nil
	case "ipp", "ipps", "http", "https":
		// The URI is the port name for an internet port; Windows accepts
		// one directly.
		return winPort{
			name:   uri,
			create: "Add-PrinterPort -Name " + psQuote(uri),
		}, nil
	case "smb":
		// \\server\share. A connection to a shared queue rather than a
		// port in its own right, but Add-Printer accepts the UNC path as
		// the port name and that is how Windows models it.
		unc := `\\` + strings.ReplaceAll(
			strings.TrimPrefix(u.Host+u.Path, "/"), "/", `\`,
		)
		return winPort{name: unc, create: "Add-PrinterPort -Name " + psQuote(unc)}, nil
	default:
		// iprint:// and anything else. Reported rather than attempted:
		// iPrint is driven by iprntcmd.exe, which is a Micro Focus client
		// tool this agent does not ship and must not assume is present.
		return winPort{}, errUnsupportedScheme{u.Scheme}
	}
}

// errBadURI is a device URI Windows cannot be pointed at.
type errBadURI struct{ uri string }

func (e errBadURI) Error() string {
	return "cannot build a printer port from " + e.uri
}

// errUnsupportedScheme is a URI scheme this platform has no provider for.
type errUnsupportedScheme struct{ scheme string }

func (e errUnsupportedScheme) Error() string {
	return e.scheme + ":// printers are not supported on this platform"
}

// psQuote renders a Go string as a PowerShell single-quoted literal.
//
// Single quotes, because PowerShell does no expansion inside them: a printer
// name containing $ or ` is data, not script. Doubling an embedded quote is
// the whole of the escaping rule for that form.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// powershell runs a script with the timeout.
func powershell(ctx context.Context, script string) (string, error) {
	return run(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script)
}
