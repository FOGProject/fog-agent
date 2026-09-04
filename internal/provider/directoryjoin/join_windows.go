//go:build windows

package directoryjoin

import (
	"context"
	"syscall"
	"unsafe"
)

// Windows joins through netapi32's NetJoinDomain.
//
// The direct call and NOT `Add-Computer` or `netdom`, and the reason is the
// credential rather than convenience. A command line is visible to every
// process on the machine through the process table, and a PowerShell script
// -- even one fed on stdin -- is captured verbatim by script block logging
// where an estate has it on, which is a place a domain join password must
// never be. NetJoinDomain takes the password as a UTF-16 pointer in this
// process's own memory: no argv, no script text, nothing on disk.
type Windows struct{}

var (
	netapi32          = syscall.NewLazyDLL("netapi32.dll")
	procNetJoinDomain = netapi32.NewProc("NetJoinDomain")
)

// NetJoinDomain options.
const (
	// NETSETUP_JOIN_DOMAIN: join a domain rather than a workgroup.
	netsetupJoinDomain = 0x00000001
	// NETSETUP_ACCT_CREATE: create the computer account if it is not
	// already there. Harmless when the account was pre-staged, which is
	// the normal arrangement in a locked-down forest.
	netsetupAcctCreate = 0x00000002
)

// Available reports whether the join API is here. It is, on every Windows
// this agent supports.
func (Windows) Available() (bool, string) {
	if err := procNetJoinDomain.Find(); err != nil {
		return false, "netapi32!NetJoinDomain was not found; this machine cannot be joined"
	}
	return true, ""
}

// Join adds the machine to the domain, creating the computer object in the
// requested OU.
//
// The OU is passed to the join rather than fixed up afterwards. Windows
// creates the object in `CN=Computers` when it is not told otherwise, and
// then the object has to be moved -- so an estate that cares where its
// machines live gets a window, however short, where a new machine sits in
// the default container inheriting whatever policy is linked there.
func (Windows) Join(ctx context.Context, p Policy) Result {
	domain, err := syscall.UTF16PtrFromString(p.Domain)
	if err != nil {
		return Result{Status: StatusFailed, Error: "domain is not usable: " + err.Error()}
	}
	account, err := syscall.UTF16PtrFromString(p.Username)
	if err != nil {
		return Result{Status: StatusFailed, Error: "username is not usable: " + err.Error()}
	}
	password, err := syscall.UTF16PtrFromString(p.Password.Reveal())
	if err != nil {
		// Deliberately does not echo err.Error(): UTF16PtrFromString's
		// failure message quotes the offending string, which here is the
		// password.
		return Result{Status: StatusFailed, Error: "the join password contains a NUL and cannot be used"}
	}

	// A nil lpAccountOU means the domain's default container, which is what
	// an empty OU on the host record should mean.
	var ou *uint16
	if p.OU != "" {
		ou, err = syscall.UTF16PtrFromString(p.OU)
		if err != nil {
			return Result{Status: StatusFailed, Error: "OU is not usable: " + err.Error()}
		}
	}

	status, _, _ := procNetJoinDomain.Call(
		0, // lpServer: nil, let Windows locate a domain controller
		uintptr(unsafe.Pointer(domain)),
		uintptr(unsafe.Pointer(ou)),
		uintptr(unsafe.Pointer(account)),
		uintptr(unsafe.Pointer(password)),
		uintptr(netsetupJoinDomain|netsetupAcctCreate),
	)
	if status != 0 {
		return Result{Status: StatusFailed, Error: joinError(uint32(status))}
	}
	// Windows always needs one: the machine is not actually operating as a
	// domain member until it restarts.
	return Result{Status: StatusJoined, Reboot: true}
}

// joinError turns a NET_API_STATUS into something an admin can act on.
//
// The named ones are the failures worth expecting, and they are named
// because the generic text does not distinguish them: syscall.Errno renders
// 1355 as "The specified domain either does not exist or could not be
// contacted", which is right, but renders the NERR_* range (2100-2999) as an
// unknown error, and NERR_SetupAlreadyJoined is the single most likely thing
// to come back here.
func joinError(status uint32) string {
	switch status {
	case 2691:
		return "already joined to a domain (NERR_SetupAlreadyJoined)"
	case 2692:
		return "not joined to a domain (NERR_SetupNotJoined)"
	case 2695:
		return "the computer name is not valid for this domain (NERR_InvalidWorkgroupName)"
	case 1326:
		return "the join credential was rejected (ERROR_LOGON_FAILURE)"
	case 1355:
		return "the domain does not exist or could not be contacted (ERROR_NO_SUCH_DOMAIN)"
	case 8557:
		return "the OU does not exist or the account may not create objects in it (ERROR_DS_NO_SUCH_OBJECT)"
	case 5:
		return "access denied; the join account lacks rights on the target OU (ERROR_ACCESS_DENIED)"
	}
	return syscall.Errno(status).Error()
}
