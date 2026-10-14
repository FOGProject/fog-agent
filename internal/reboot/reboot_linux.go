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

// osReboot uses shutdown(8) for a delayed action, which also broadcasts the
// message to logged-in users; an immediate one goes through systemctl, or
// the kernel where there is no systemd.
func osReboot(mode string, delay time.Duration, message string) error {
	flag, unit, kernel := "-r", "reboot", syscall.LINUX_REBOOT_CMD_RESTART
	if mode == ModeShutdown {
		flag, unit, kernel = "-h", "poweroff", syscall.LINUX_REBOOT_CMD_POWER_OFF
	}
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
		out, err := exec.Command(path, flag, fmt.Sprintf("+%d", minutes), message).CombinedOutput()
		if err != nil {
			// logind refuses a scheduled shutdown while a block inhibitor
			// lock is held (issue #30). The refusal reads "Operation denied
			// due to active block inhibitor" or "Access denied", depending
			// on the systemd version, so any refusal tries the countdown
			// that does not go through logind.
			if terr := scheduleIgnoringInhibitors(unit, delay, message); terr == nil || errors.Is(terr, ErrInProgress) {
				return terr
			}
			return errors.New(strings.TrimSpace(string(out)))
		}
		return nil
	}
	if path, err := exec.LookPath("systemctl"); err == nil {
		out, err := exec.Command(path, unit).CombinedOutput()
		if err != nil {
			// A block inhibitor lock makes logind refuse (issue #30).
			// The coordinator has already applied the reboot policy, and
			// Windows overrides the same way with shutdown.exe /f; a lock
			// nobody releases would otherwise hold the machine, and any
			// task waiting on it, forever.
			if ferr := exec.Command(path, forceArgs(unit)...).Run(); ferr == nil {
				return nil
			}
			return errors.New(strings.TrimSpace(string(out)))
		}
		return nil
	}
	syscall.Sync()
	return syscall.Reboot(kernel)
}

// timerUnit names the countdown scheduleIgnoringInhibitors starts, so an
// admin can cancel it with `systemctl stop fog-agent-reboot.timer`.
const timerUnit = "fog-agent-reboot"

// forceArgs are the systemctl arguments that reboot or power off without
// asking logind, which is where inhibitor locks are enforced. Root cannot
// skip them through logind either: `systemctl -i` there becomes a polkit
// question that needs an interactive answer (measured on systemd 257).
// Starting the target is what logind itself does once it agrees, so the
// shutdown is the normal, orderly one.
func forceArgs(unit string) []string {
	return []string{"start", "--no-block", "--job-mode=replace-irreversibly", unit + ".target"}
}

// scheduleIgnoringInhibitors is the delayed action for a machine with a
// block inhibitor lock. logind's ScheduleShutdown, which shutdown(8) uses,
// enforces the lock, so the countdown runs as a transient timer owned by
// PID 1 instead. It survives an agent restart and ends in forceArgs. wall
// sends the warning shutdown(8) would have sent.
func scheduleIgnoringInhibitors(unit string, delay time.Duration, message string) error {
	run, err := exec.LookPath("systemd-run")
	if err != nil {
		return err
	}
	ctl, err := exec.LookPath("systemctl")
	if err != nil {
		return err
	}
	seconds := int((delay + time.Second - 1) / time.Second)
	args := append([]string{"--unit=" + timerUnit,
		fmt.Sprintf("--on-active=%ds", seconds), "--timer-property=AccuracySec=1s", ctl},
		forceArgs(unit)...)
	out, err := exec.Command(run, args...).CombinedOutput()
	if err != nil {
		// A countdown from an earlier poll is still running.
		if bytes.Contains(out, []byte("already loaded")) {
			return ErrInProgress
		}
		return errors.New(strings.TrimSpace(string(out)))
	}
	if w, err := exec.LookPath("wall"); err == nil {
		_ = exec.Command(w, message).Run()
	}
	return nil
}
