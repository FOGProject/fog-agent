//go:build linux

package reboot

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// osLoggedIn asks logind, which sees graphical, console and ssh sessions
// alike and is what modern distributions maintain (Debian 13 has retired
// utmp); `who` is the fallback for a box without systemd.
//
// Sessions, not users: logind also books the per-user service manager
// (class "manager", systemd 256+) and greeters or lock screens as
// sessions, and a user's manager outlives their last login by a moment.
// Only the classes a person sits in count.
func osLoggedIn() (int, error) {
	if path, err := exec.LookPath("loginctl"); err == nil {
		out, err := exec.Command(path, "list-sessions", "--no-legend").Output()
		if err == nil {
			return countUserSessions(out), nil
		}
	}
	if path, err := exec.LookPath("who"); err == nil {
		out, err := exec.Command(path).Output()
		if err != nil {
			return 0, err
		}
		users := map[string]bool{}
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			if f := strings.Fields(sc.Text()); len(f) > 0 {
				users[f[0]] = true
			}
		}
		return len(users), nil
	}
	return 0, errors.New("neither loginctl nor who is available")
}

// countUserSessions reads `loginctl list-sessions --no-legend`, whose
// columns are SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE on systemd
// 256+ and SESSION UID USER SEAT TTY before that. Older output has no class
// column and lists only real sessions, so every row counts there.
func countUserSessions(out []byte) int {
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) >= 6 && !strings.HasPrefix(f[5], "user") {
			continue
		}
		n++
	}
	return n
}

// osReboot uses shutdown(8) for a delayed reboot, which also broadcasts the
// message to logged-in users; an immediate one goes through systemctl, or
// the kernel where there is no systemd.
func osReboot(delay time.Duration, message string) error {
	if delay > 0 {
		// A service's PATH does not always reach the sbin directories
		// shutdown lives in.
		path, err := exec.LookPath("shutdown")
		for _, p := range []string{"/usr/sbin/shutdown", "/sbin/shutdown"} {
			if err == nil {
				break
			}
			path, err = exec.LookPath(p)
		}
		if err != nil {
			return err
		}
		// shutdown counts in whole minutes; round up so nobody gets
		// less warning than the policy promised.
		minutes := int((delay + time.Minute - 1) / time.Minute)
		out, err := exec.Command(path, "-r", fmt.Sprintf("+%d", minutes), message).CombinedOutput()
		if err != nil {
			return errors.New(strings.TrimSpace(string(out)))
		}
		return nil
	}
	if path, err := exec.LookPath("systemctl"); err == nil {
		out, err := exec.Command(path, "reboot").CombinedOutput()
		if err != nil {
			return errors.New(strings.TrimSpace(string(out)))
		}
		return nil
	}
	syscall.Sync()
	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
}
