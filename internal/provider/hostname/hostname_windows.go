//go:build windows

package hostname

import (
	"os"
	"syscall"
	"unsafe"
)

// computerNamePhysicalDnsHostname is the COMPUTER_NAME_FORMAT that sets
// the DNS host name and, with it, the NetBIOS name. The change takes
// effect at the next boot, which is why the provider answers
// pending_reboot rather than applied.
const computerNamePhysicalDnsHostname = 5

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procSetComputerNameEx = kernel32.NewProc("SetComputerNameExW")
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
