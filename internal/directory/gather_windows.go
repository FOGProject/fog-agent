//go:build windows

package directory

import (
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// NETSETUP_JOIN_STATUS, as NetGetJoinInformation returns it.
const (
	netSetupUnknownStatus = 0
	netSetupUnjoined      = 1
	netSetupWorkgroupName = 2
	netSetupDomainName    = 3
)

// EXTENDED_NAME_FORMAT values for GetComputerObjectName.
const (
	nameFullyQualifiedDN = 1
	nameSamCompatible    = 2
)

// COMPUTER_NAME_FORMAT: the DNS domain this machine belongs to.
const computerNameDnsDomain = 2

var (
	netapi32                   = syscall.NewLazyDLL("netapi32.dll")
	procNetGetJoinInformation  = netapi32.NewProc("NetGetJoinInformation")
	procNetAPIBufferFree       = netapi32.NewProc("NetApiBufferFree")
	procDsGetSiteName          = netapi32.NewProc("DsGetSiteNameW")
	secur32                    = syscall.NewLazyDLL("secur32.dll")
	procGetComputerObjectNameW = secur32.NewProc("GetComputerObjectNameW")
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procGetComputerNameExW     = kernel32.NewProc("GetComputerNameExW")

	// Replaced in tests; dsregcmd is the only probe here that is a command
	// rather than a call, because Entra join state has no stable API.
	runDsregcmd = func() (string, bool) {
		out, err := exec.Command("dsregcmd", "/status").Output()
		if err != nil {
			return "", false
		}
		return string(out), true
	}
)

// gather asks Windows directly rather than shelling out. NetGetJoinInformation
// is the authoritative answer to "am I joined", and unlike the legacy
// client's IsJoinedToDomain -- which resolved both domain names to IPs and
// intersected the sets (design 0009 §1.1) -- it cannot be confused by DNS.
func gather() (Directory, bool) {
	status, name, ok := joinInformation()
	if !ok || status == netSetupUnknownStatus {
		// Windows declined to say. Report nothing rather than guess: a
		// false "unjoined" would put the host into drift (design 0009 §3).
		return Directory{}, false
	}

	if status != netSetupDomainName {
		d := Directory{Joined: false, Kind: KindWorkgroup}
		if status == netSetupUnjoined {
			d.Kind = KindNone
		}
		if status == netSetupWorkgroupName {
			// An Entra-joined machine reports a workgroup here, because it
			// has no on-premises domain. Asking dsregcmd is the only way to
			// tell it from a genuinely standalone machine, and mis-filing
			// one as unjoined is exactly what design 0009 §9 rules out.
			if out, ok := runDsregcmd(); ok && entraJoined(out) {
				d.Joined = true
				d.Kind = KindEntra
				d.Domain = dsregValue(out, "TenantName")
			}
		}
		_ = name
		return d, true
	}

	d := Directory{Joined: true, Kind: KindAD, Netbios: name}
	// The DNS domain, which NetGetJoinInformation does not reliably give.
	d.Domain = computerNameEx(computerNameDnsDomain)
	// The computer object's own DN, straight from the machine's secure
	// channel -- no credential, because the machine account is reading its
	// own object. This is the field a server-side Modify DN needs.
	d.ComputerDN = computerObjectName(nameFullyQualifiedDN)
	// CORP\WS-014$ -- take the part after the backslash.
	if sam := computerObjectName(nameSamCompatible); sam != "" {
		if _, acct, found := strings.Cut(sam, `\`); found {
			d.MachineAccount = acct
		} else {
			d.MachineAccount = sam
		}
	}
	d.Site = siteName()
	return d, true
}

// joinInformation wraps NetGetJoinInformation. The name buffer it fills is
// allocated by the network API and must go back through NetApiBufferFree,
// not Go's allocator.
func joinInformation() (uint32, string, bool) {
	var (
		buf    *uint16
		status uint32
	)
	r, _, _ := procNetGetJoinInformation.Call(
		0,
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&status)),
	)
	if r != 0 {
		return 0, "", false
	}
	name := ""
	if buf != nil {
		name = utf16PtrToString(buf)
		procNetAPIBufferFree.Call(uintptr(unsafe.Pointer(buf)))
	}
	return status, name, true
}

// computerObjectName wraps GetComputerObjectName, which answers for the
// machine account rather than the caller. Two calls: the first sizes the
// buffer, the second fills it.
func computerObjectName(format uintptr) string {
	var size uint32
	procGetComputerObjectNameW.Call(format, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return ""
	}
	buf := make([]uint16, size)
	r, _, _ := procGetComputerObjectNameW.Call(
		format,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// computerNameEx wraps GetComputerNameEx, same two-call sizing dance. An
// empty answer is normal on a machine with no DNS domain.
func computerNameEx(format uintptr) string {
	var size uint32
	procGetComputerNameExW.Call(format, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return ""
	}
	buf := make([]uint16, size+1)
	size = uint32(len(buf))
	r, _, _ := procGetComputerNameExW.Call(
		format,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// siteName wraps DsGetSiteName. It fails on a machine that has not resolved
// a site, which is not an error worth reporting -- the field is optional.
func siteName() string {
	var buf *uint16
	r, _, _ := procDsGetSiteName.Call(0, uintptr(unsafe.Pointer(&buf)))
	if r != 0 || buf == nil {
		return ""
	}
	name := utf16PtrToString(buf)
	procNetAPIBufferFree.Call(uintptr(unsafe.Pointer(buf)))
	return name
}

// utf16PtrToString reads a NUL-terminated UTF-16 string out of a buffer the
// network API allocated, without taking ownership of it.
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	var out []uint16
	for i := 0; ; i++ {
		c := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(i)*2))
		if c == 0 {
			break
		}
		out = append(out, c)
	}
	return syscall.UTF16ToString(out)
}

// entraJoined reads dsregcmd's "AzureAdJoined : YES" line. Only the joined
// flag matters here; a device that is merely Entra-*registered* (a personal
// machine with a work account added) is not a managed membership and must
// not be reported as one.
func entraJoined(out string) bool {
	return strings.EqualFold(dsregValue(out, "AzureAdJoined"), "YES")
}

// dsregValue pulls one `Key : Value` line out of dsregcmd's output.
func dsregValue(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
