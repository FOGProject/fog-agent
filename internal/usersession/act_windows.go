//go:build windows

package usersession

import (
	"fmt"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procWTSLogoffSession = wtsapi32.NewProc("WTSLogoffSession")
	procWTSSendMessage   = wtsapi32.NewProc("WTSSendMessageW")
)

// MB_OK | MB_ICONEXCLAMATION | MB_SYSTEMMODAL -- the message has to be
// visible over a full-screen window, because the whole point is reaching
// somebody who is not looking.
const wtsMessageStyle = 0x00000000 | 0x00000030 | 0x00001000

// idleFor resolves the string session key the collectors publish back to the
// numeric session id the WTS API wants.
func idleFor(key string) (time.Duration, bool) {
	id, err := strconv.ParseUint(key, 10, 32)
	if err != nil {
		return 0, false
	}
	return sessionIdle(uint32(id))
}

// logoff ends a session. bWait is FALSE: a logoff can take as long as the
// slowest application's shutdown handler, and the agent's sample loop must
// not be held for it. The session going away is observed on the next sample
// like any other logoff, so nothing depends on this call blocking.
func logoff(key string) error {
	id, err := strconv.ParseUint(key, 10, 32)
	if err != nil {
		return fmt.Errorf("usersession: bad session key %q", key)
	}
	r, _, callErr := procWTSLogoffSession.Call(
		uintptr(wtsCurrentServer),
		uintptr(uint32(id)),
		0, // bWait FALSE
	)
	if r == 0 {
		if callErr == windows.ERROR_FILE_NOT_FOUND || callErr == windows.ERROR_NO_SUCH_LOGON_SESSION {
			// Gone between the sample and here: the wanted outcome.
			return nil
		}
		return fmt.Errorf("WTSLogoffSession(%s): %w", key, callErr)
	}
	return nil
}

// notify puts a message box on the session's desktop.
//
// This is how a service in session 0 talks to a user, and it is the reason
// the agent needs no tray application: WTSSendMessage is rendered by
// winlogon inside the target session, so it crosses the session-0 isolation
// boundary that has made every "service pops up a dialog" approach fail
// since Vista.
//
// bWait is FALSE and the timeout is zero, which together mean "show it and
// return immediately, leaving the box up". Waiting for the user to click OK
// would block the sample loop for as long as they are away -- which, since
// they are away by definition, is the whole timeout.
func notify(key, message string) bool {
	id, err := strconv.ParseUint(key, 10, 32)
	if err != nil {
		return false
	}
	title, err := windows.UTF16FromString("FOG")
	if err != nil {
		return false
	}
	body, err := windows.UTF16FromString(message)
	if err != nil {
		return false
	}
	var response uint32
	// The lengths are BYTES, not characters, and exclude the terminating
	// NUL. Passing the element count shows half the message and is the
	// classic way to get this call subtly wrong.
	r, _, _ := procWTSSendMessage.Call(
		uintptr(wtsCurrentServer),
		uintptr(uint32(id)),
		uintptr(unsafe.Pointer(&title[0])),
		uintptr((len(title)-1)*2),
		uintptr(unsafe.Pointer(&body[0])),
		uintptr((len(body)-1)*2),
		uintptr(wtsMessageStyle),
		0, // timeout: 0 means the box stays until dismissed
		uintptr(unsafe.Pointer(&response)),
		0, // bWait FALSE
	)
	return r != 0
}
