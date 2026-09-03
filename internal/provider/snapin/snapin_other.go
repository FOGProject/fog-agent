//go:build !linux && !darwin && !windows

package snapin

import (
	"context"
	"errors"
	"os/exec"
)

func command(context.Context, Task, string) (*exec.Cmd, error) {
	return nil, errors.New("snapins are not supported on this platform")
}
