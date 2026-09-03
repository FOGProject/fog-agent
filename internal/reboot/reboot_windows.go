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
	procWTSFreeMemory        = wtsapi32.NewProc("WTSFreeMemory")
)

// wtsSessionInfo mirrors WTS_SESSION_INFOW.
type wtsSessionInfo struct {
	sessionID  uint32
	winStation *uint16
	state      uint32
}

const (
	wtsActive       = 0
	wtsDisconnected = 4
)

// osLoggedIn counts interactive sessions that hold a user: active ones and
// disconnected ones, since a disconnected RDP session still has that
// person's work open. Session 0 (services) never appears as either.
func osLoggedIn() (int, error) {
	var info *wtsSessionInfo
	var count uint32
	r, _, e := procWTSEnumerateSessions.Call(0, 0, 1, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&count)))
	if r == 0 {
		return 0, e
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(info)))
	sessions := unsafe.Slice(info, count)
	n := 0
	for _, s := range sessions {
		if s.state == wtsActive || s.state == wtsDisconnected {
			n++
		}
	}
	return n, nil
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
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}
