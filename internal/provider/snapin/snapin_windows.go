//go:build windows

package snapin

import (
	"context"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// command builds the run the way the legacy client did: the interpreter
// (cmd.exe, msiexec.exe, powershell.exe, ...) with its own arguments, then
// the payload, then the snapin's arguments; or the payload itself when no
// interpreter is named.
func command(ctx context.Context, t Task, path string) (*exec.Cmd, error) {
	if t.RunWith == "" {
		return raw(ctx, path, t.Args)
	}
	return raw(ctx, t.RunWith, strings.TrimSpace(t.RunWithArgs)+` "`+path+`" `+t.Args)
}

// packCommand builds a pack's run: RunWith with RunWithArgs, the
// placeholder replaced by dir, as the legacy client did.
func packCommand(ctx context.Context, t Task, dir string) (*exec.Cmd, error) {
	r := strings.NewReplacer(Placeholder, dir)
	return raw(ctx, r.Replace(t.RunWith), r.Replace(t.RunWithArgs))
}

// raw runs name with args as one command line, untouched but for
// %VAR% expansion. Snapin arguments were written for the legacy client,
// which handed them to CreateProcess as typed. Splitting them and
// re-quoting each part breaks two things: a backslash in a path, and
// msiexec's PROPERTY="a b" form, which it only reads unquoted up to the
// "=".
func raw(ctx context.Context, name, args string) (*exec.Cmd, error) {
	name, err := expand(name)
	if err != nil {
		return nil, err
	}
	if args, err = expand(strings.TrimSpace(args)); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name)
	line := syscall.EscapeArg(name)
	if args != "" {
		line += " " + args
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	return cmd, nil
}

// expand replaces %VAR% references with the agent's environment, as
// the legacy client's Environment.ExpandEnvironmentVariables did.
func expand(s string) (string, error) {
	if !strings.Contains(s, "%") {
		return s, nil
	}
	src, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return "", err
	}
	for size := uint32(len(s) + 1); ; {
		buf := make([]uint16, size)
		n, err := windows.ExpandEnvironmentStrings(src, &buf[0], size)
		if err != nil {
			return "", err
		}
		if n <= size {
			return windows.UTF16ToString(buf[:n]), nil
		}
		size = n
	}
}
