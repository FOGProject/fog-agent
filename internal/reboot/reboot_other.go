//go:build !linux && !windows && !darwin

package reboot

import (
	"errors"
	"time"
)

func osLoggedIn() (int, error) { return 0, errors.New("not supported on this platform") }

func osReboot(time.Duration, string) error { return errors.New("not supported on this platform") }
