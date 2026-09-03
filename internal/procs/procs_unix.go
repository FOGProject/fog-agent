//go:build unix

package procs

import (
	"os/exec"
	"syscall"
)

// Attach puts cmd in its own process group and makes a context cancel
// kill the whole group, so a payload that spawned children does not
// leave them running past a timeout.
func Attach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
