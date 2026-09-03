//go:build !linux && !windows && !darwin

package hostname

import (
	"errors"
	"os"
)

func osCurrent() (string, error) {
	return os.Hostname()
}

func osSet(string) (bool, error) {
	return false, errors.New("hostname changes are not supported on this platform")
}
