//go:build windows

package usersession

import (
	"fmt"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wtsapi32             = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSEnumSessions  = wtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQuerySession  = wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory    = wtsapi32.NewProc("WTSFreeMemory")
	wtsCurrentServer     = windows.Handle(0)
	errNoSessionInfoSize = fmt.Errorf("session info buffer too small")
)

// WTS information classes (wtsapi32.h). Only the ones design 0008 stores.
const (
	wtsUserName      = 5
	wtsDomainName    = 7
	wtsClientAddress = 14
	wtsSessionInfo   = 24
)

// WTS_CONNECTSTATE_CLASS values.
const (
	wtsActive       = 0
	wtsDisconnected = 4
)

// wtsSessionInfoW mirrors WTS_SESSION_INFOW.
type wtsSessionInfoW struct {
	SessionID      uint32
	WinStationName *uint16
	State          uint32
}

// wtsInfoW mirrors WTSINFOW. The only field worth the struct is LogonTime:
// UserName here is capped at 20 characters, so the name and domain are read
// through WTSQuerySessionInformation instead, which is not capped.
//
// The layout is load-bearing (LogonTime sits at offset 200 of 216 on amd64),
// so it is asserted at compile time below rather than trusted.
type wtsInfoW struct {
	State                   uint32
	SessionID               uint32
	IncomingBytes           uint32
	OutgoingBytes           uint32
	IncomingFrames          uint32
	OutgoingFrames          uint32
	IncomingCompressedBytes uint32
	OutgoingCompressedBytes uint32
	WinStationName          [32]uint16
	Domain                  [17]uint16
	UserName                [21]uint16
	// Explicit padding, and it is not decoration. The fields above end at
	// offset 172. Windows lays the int64s out on an 8-byte boundary on both
	// 32- and 64-bit, so the real struct pads to 176. Go's amd64 alignment
	// does that by itself, but on 386 Go aligns int64 to 4, so without this
	// the struct is 212 bytes and every timestamp below is read four bytes
	// early -- a plausible wrong answer, not a crash. Writing the pad out
	// makes both architectures agree with the header.
	_              [4]byte
	ConnectTime    int64
	DisconnectTime int64
	LastInputTime  int64
	LogonTime      int64
	CurrentTime    int64
}

// A wrong size means the compiler laid the struct out differently from the
// Windows header and LogonTime would be read from the wrong offset -- which
// yields a plausible-looking wrong timestamp rather than an error. Same
// guard, and the same reason, as memoryStatusEx in internal/inventory.
var _ = [1]struct{}{}[unsafe.Sizeof(wtsInfoW{})-216]

// wtsClientAddressS mirrors WTS_CLIENT_ADDRESS.
type wtsClientAddressS struct {
	AddressFamily uint32
	Address       [20]byte
}

// list enumerates the host's terminal-services sessions, which is every
// logon: console and RDP alike.
//
// This runs inside a service, in session 0. Design 0006 section 6 recorded
// EnumDisplayDevices returning FALSE with GetLastError == ERROR_SUCCESS there
// because a service has no window station -- a silent empty result that looked
// exactly like a machine with no GPU. The WTS calls are session-management
// APIs and do not touch a window station, and the lab probe confirms they
// enumerate real sessions from session 0; that probe is the evidence, not the
// documentation.
func list() ([]Session, bool) {
	var raw *wtsSessionInfoW
	var count uint32
	r, _, err := procWTSEnumSessions.Call(
		uintptr(wtsCurrentServer),
		0, // Reserved, must be 0
		1, // Version, must be 1
		uintptr(unsafe.Pointer(&raw)),
		uintptr(unsafe.Pointer(&count)),
	)
	if r == 0 {
		_ = err
		return nil, false
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(raw)))

	infos := unsafe.Slice(raw, count)
	var out []Session
	for _, si := range infos {
		// Session 0 is the services session; it never hosts a user logon.
		if si.SessionID == 0 {
			continue
		}
		if si.State != wtsActive && si.State != wtsDisconnected {
			continue
		}
		user := queryString(si.SessionID, wtsUserName)
		if user == "" {
			// A listener or an unoccupied station: a session exists but
			// nobody is logged into it.
			continue
		}
		s := Session{
			Key:        fmt.Sprintf("%d", si.SessionID),
			User:       user,
			Domain:     queryString(si.SessionID, wtsDomainName),
			Type:       stationType(windows.UTF16PtrToString(si.WinStationName)),
			State:      connectState(si.State),
			RemoteHost: clientAddress(si.SessionID),
			StartedAt:  logonTime(si.SessionID),
		}
		s.SID = lookupSID(s.Domain, s.User)
		out = append(out, s)
	}
	return out, true
}

// queryString reads one string information class for a session. WTS returns a
// buffer it owns, so it is copied out before being freed.
func queryString(session uint32, class uint32) string {
	var buf *uint16
	var n uint32
	r, _, _ := procWTSQuerySession.Call(
		uintptr(wtsCurrentServer),
		uintptr(session),
		uintptr(class),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&n)),
	)
	if r == 0 || buf == nil {
		return ""
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf)
}

// sessionInfo reads WTSINFO for one session and copies it out of the buffer
// WTS owns. The copy matters: the caller keeps the value after WTSFreeMemory
// has run, and reading through the pointer afterwards is a use-after-free
// that would usually appear to work.
func sessionInfo(session uint32) (wtsInfoW, bool) {
	var buf *wtsInfoW
	var n uint32
	r, _, _ := procWTSQuerySession.Call(
		uintptr(wtsCurrentServer),
		uintptr(session),
		uintptr(wtsSessionInfo),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&n)),
	)
	if r == 0 || buf == nil {
		return wtsInfoW{}, false
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
	if n < uint32(unsafe.Sizeof(wtsInfoW{})) {
		_ = errNoSessionInfoSize
		return wtsInfoW{}, false
	}
	return *buf, true
}

// logonTime converts the FILETIME logon stamp. A failure yields the zero
// time, never "now": design 0008 treats StartedAt as the OS's exact record,
// and a fabricated one silently becomes a session duration.
func logonTime(session uint32) time.Time {
	info, ok := sessionInfo(session)
	if !ok {
		return time.Time{}
	}
	return filetimeToTime(info.LogonTime)
}

// idleFor reports how long a session has had no user input, from WTSINFO's
// own pair of stamps rather than by comparing one of them to the local
// clock -- CurrentTime and LastInputTime come from the same read, so the
// difference is not exposed to clock skew or to the time the call took.
//
// The false return is doing real work. LastInputTime is not populated for
// every session on every Windows build; a console session in particular can
// report zero, and there is no supported alternative from session 0
// (GetLastInputInfo answers only for the caller's own session, and a service
// has none). Zero means "not known", and it MUST NOT be read as an epoch:
// treating it as 1601 makes every session infinitely idle, which for the
// auto log out capability means logging the whole fleet off at once. So an
// unusable pair is reported as unknown and the caller does nothing.
func sessionIdle(session uint32) (time.Duration, bool) {
	info, ok := sessionInfo(session)
	if !ok {
		return 0, false
	}
	if info.LastInputTime <= 0 || info.CurrentTime <= 0 {
		return 0, false
	}
	d := time.Duration(info.CurrentTime-info.LastInputTime) * 100 * time.Nanosecond
	if d < 0 {
		// Input recorded after the snapshot: the session is active now.
		return 0, true
	}
	return d, true
}

// filetimeToTime converts a FILETIME (100ns ticks since 1601-01-01 UTC).
func filetimeToTime(ft int64) time.Time {
	if ft <= 0 {
		return time.Time{}
	}
	ftw := windows.Filetime{
		LowDateTime:  uint32(ft & 0xFFFFFFFF),
		HighDateTime: uint32(ft >> 32),
	}
	return time.Unix(0, ftw.Nanoseconds()).UTC()
}

// clientAddress renders the remote endpoint of an RDP session. A console
// session has no client address and returns "".
func clientAddress(session uint32) string {
	var buf *wtsClientAddressS
	var n uint32
	r, _, _ := procWTSQuerySession.Call(
		uintptr(wtsCurrentServer),
		uintptr(session),
		uintptr(wtsClientAddress),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&n)),
	)
	if r == 0 || buf == nil {
		return ""
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))

	switch buf.AddressFamily {
	case uint32(windows.AF_INET):
		// The four octets sit at offset 2; the leading two bytes are the
		// port field of the sockaddr this was carved from.
		ip := net.IPv4(buf.Address[2], buf.Address[3], buf.Address[4], buf.Address[5])
		if ip.IsUnspecified() {
			return ""
		}
		return ip.String()
	case uint32(windows.AF_INET6):
		ip := net.IP(buf.Address[:16])
		if ip.IsUnspecified() {
			return ""
		}
		return ip.String()
	}
	return ""
}

// lookupSID resolves DOMAIN\user to a SID. Best effort: an offline domain
// member cannot resolve a domain account, and an empty SID is honest where a
// wrong one would be worse.
func lookupSID(domain, user string) string {
	name := user
	if domain != "" {
		name = domain + `\` + user
	}
	sid, _, _, err := windows.LookupSID("", name)
	if err != nil || sid == nil {
		return ""
	}
	return sid.String()
}

func connectState(state uint32) string {
	if state == wtsDisconnected {
		return StateDisconnect
	}
	return StateActive
}
