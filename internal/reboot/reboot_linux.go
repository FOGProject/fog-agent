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
func osLoggedIn() (int, error) {
	if path, err := exec.LookPath("loginctl"); err == nil {
		out, err := exec.Command(path, "list-users", "--no-legend").Output()
		if err == nil {
			return countLines(out), nil
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

func countLines(out []byte) int {
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
}

// osReboot uses shutdown(8) for a delayed reboot, which also broadcasts the
// message to logged-in users; an immediate one goes through systemctl, or
// the kernel where there is no systemd.
func osReboot(delay time.Duration, message string) error {
	if delay > 0 {
		path, err := exec.LookPath("shutdown")
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
