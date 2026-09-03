//go:build windows

package snapin

import (
	"context"
	"os/exec"
)

// command builds the run the way the legacy client did: the interpreter
// (cmd.exe, msiexec.exe, powershell.exe, ...) with its own arguments, then
// the payload, then the snapin's arguments; or the payload itself when no
// interpreter is named.
func command(ctx context.Context, t Task, path string) (*exec.Cmd, error) {
	if t.RunWith == "" {
		return exec.CommandContext(ctx, path, splitArgs(t.Args)...), nil
	}
	args := append(splitArgs(t.RunWithArgs), path)
	args = append(args, splitArgs(t.Args)...)
	return exec.CommandContext(ctx, t.RunWith, args...), nil
}
