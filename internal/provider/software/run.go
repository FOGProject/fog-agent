package software

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/FOGProject/fog-agent/internal/procs"
)

// runCmd executes the program at path with args under timeout seconds (0
// for none). The Status is empty when the program ran and the exit code is
// its own. Every backend runs its commands through here.
func runCmd(ctx context.Context, path string, timeout int, args ...string) Result {
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	}
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, args...)
	procs.Attach(cmd)
	out := procs.NewTail(MaxDetails)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	r := Result{Details: out.String()}
	switch {
	case err == nil:
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		r.Status = StatusTimeout
		r.Details = fmt.Sprintf("timed out after %ds\n%s", timeout, r.Details)
	default:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			r.ExitCode = exit.ExitCode()
		} else {
			r.Status = StatusCannotRun
			r.Details = err.Error() + "\n" + r.Details
		}
	}
	r.Details = procs.Clip(r.Details, MaxDetails)
	return r
}

// lookup says whether the program at path can be found. A bare path is a
// command name looked up on PATH; anything else is stat'ed. what names the
// program in the message.
func lookup(path, what string) (bool, string) {
	if filepath.Base(path) == path {
		if _, err := exec.LookPath(path); err != nil {
			return false, what + " is not installed: " + path + " is not on PATH"
		}
		return true, ""
	}
	if _, err := os.Stat(path); err != nil {
		return false, what + " is not installed: " + path + " is missing"
	}
	return true, ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
