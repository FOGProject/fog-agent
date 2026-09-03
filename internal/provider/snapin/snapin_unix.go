//go:build linux || darwin

package snapin

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// command builds the run: the interpreter with its own arguments, then
// the payload, then the snapin's arguments -- or the payload itself,
// made executable, when the snapin names no interpreter.
func command(ctx context.Context, t Task, path string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	if t.RunWith == "" {
		if err := os.Chmod(path, 0o700); err != nil {
			return nil, err
		}
		cmd = exec.CommandContext(ctx, path, splitArgs(t.Args)...)
	} else {
		args := append(splitArgs(t.RunWithArgs), path)
		args = append(args, splitArgs(t.Args)...)
		cmd = exec.CommandContext(ctx, t.RunWith, args...)
	}
	// Its own process group, killed as a group on timeout: a script's
	// children (an installer it launched) go with it, and cannot keep the
	// task alive past the deadline the snapin was given.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd, nil
}
