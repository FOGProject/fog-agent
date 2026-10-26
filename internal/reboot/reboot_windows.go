//go:build windows

package reboot

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	wtsapi32                 = syscall.NewLazyDLL("wtsapi32.dll")
	procWTSEnumerateSessions = wtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQuerySessionInfo  = wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory        = wtsapi32.NewProc("WTSFreeMemory")
)

// wtsSessionInfo mirrors WTS_SESSION_INFOW.
type wtsSessionInfo struct {
	sessionID  uint32
	winStation *uint16
	state      uint32
}

const wtsUserName = 5

// osLoggedIn counts active and disconnected interactive sessions with a
// user name. A disconnected RDP session still holds that person's work.
func osLoggedIn() (int, error) {
	var info *wtsSessionInfo
	var count uint32
	r, _, e := procWTSEnumerateSessions.Call(0, 0, 1, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&count)))
	if r == 0 {
		return 0, wtsCallError("WTSEnumerateSessionsW", e)
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(info)))
	sessions := unsafe.Slice(info, count)
	portable := make([]session, len(sessions))
	for i, s := range sessions {
		portable[i] = session{id: s.sessionID, state: s.state}
	}
	return countLoggedInSessions(portable, wtsUserNameForSession)
}

func wtsUserNameForSession(sessionID uint32) (string, error) {
	var buffer *uint16
	var bytes uint32
	r, _, e := procWTSQuerySessionInfo.Call(0, uintptr(sessionID), wtsUserName, uintptr(unsafe.Pointer(&buffer)), uintptr(unsafe.Pointer(&bytes)))
	if buffer != nil {
		defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buffer)))
	}
	if r == 0 {
		return "", wtsCallError("WTSQuerySessionInformationW", e)
	}
	if buffer == nil || bytes < 2 || bytes%2 != 0 {
		return "", errMalformedWTSUserName
	}
	return decodeWTSUserName(unsafe.Slice(buffer, bytes/2), bytes)
}

func wtsCallError(operation string, callErr error) error {
	if callErr == nil || callErr == syscall.Errno(0) {
		return fmt.Errorf("%s failed", operation)
	}
	return fmt.Errorf("%s failed: %w", operation, callErr)
}

// osReboot goes through shutdown.exe, which enables SeShutdownPrivilege for
// itself, shows the message to every session and honors the countdown.
// /d p:0:0 marks the reason planned, so it does not land in the event log
// as an unexpected shutdown.
func osReboot(mode string, delay time.Duration, message string) error {
	path, err := exec.LookPath("shutdown.exe")
	if err != nil {
		return err
	}
	flag := "/r"
	if mode == ModeShutdown {
		flag = "/s"
	}
	out, err := exec.Command(path, flag, "/f", "/t", fmt.Sprint(int(delay/time.Second)), "/d", "p:0:0", "/c", message).CombinedOutput()
	if err != nil {
		// shutdown.exe exits with the Win32 error code.
		// ERROR_SHUTDOWN_IN_PROGRESS is 1115.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1115 {
			return ErrInProgress
		}
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}
