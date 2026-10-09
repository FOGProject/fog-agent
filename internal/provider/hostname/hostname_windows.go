//go:build windows

package hostname

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// computerNamePhysicalDnsHostname is the COMPUTER_NAME_FORMAT that sets
// the DNS host name and, with it, the NetBIOS name. The change takes
// effect at the next boot, which is why the provider answers
// pending_reboot rather than applied.
const computerNamePhysicalDnsHostname = 5

// netSetupDomainName is NetGetJoinInformation's NETSETUP_JOIN_STATUS for
// a member of an AD domain.
const netSetupDomainName = 3

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procSetComputerNameEx     = kernel32.NewProc("SetComputerNameExW")
	netapi32                  = syscall.NewLazyDLL("netapi32.dll")
	procNetGetJoinInformation = netapi32.NewProc("NetGetJoinInformation")
	procNetAPIBufferFree      = netapi32.NewProc("NetApiBufferFree")
)

func osCurrent() (string, error) {
	return os.Hostname()
}

func osSet(name string) (bool, error) {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	r, _, e := procSetComputerNameEx.Call(computerNamePhysicalDnsHostname, uintptr(unsafe.Pointer(p)))
	if r == 0 {
		return false, e
	}
	return true, nil
}

// osJoined asks NetGetJoinInformation. A call that fails answers true: a
// machine whose membership is unknown is not renamed alone, because on a
// domain member that is the rename that breaks it.
func osJoined() bool {
	var (
		buf    *uint16
		status uint32
	)
	r, _, _ := procNetGetJoinInformation.Call(0,
		uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&status)))
	if r != 0 {
		return true
	}
	if buf != nil {
		procNetAPIBufferFree.Call(uintptr(unsafe.Pointer(buf)))
	}
	return status == netSetupDomainName
}

// osPending reads the DNS host name set for the next boot. SetComputerNameEx
// writes it here, and os.Hostname keeps answering with the running name
// until the reboot. Empty when it cannot be read.
func osPending() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("NV Hostname")
	if err != nil {
		return ""
	}
	return v
}
