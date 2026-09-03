//go:build darwin

package hostname

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

func osCurrent() (string, error) {
	return os.Hostname()
}

// osSet sets all three names macOS keeps, as the old client did.
func osSet(name string) (bool, error) {
	for _, key := range []string{"HostName", "LocalHostName", "ComputerName"} {
		out, err := exec.Command("scutil", "--set", key, name).CombinedOutput()
		if err != nil {
			return false, errors.New(key + ": " + strings.TrimSpace(string(out)))
		}
	}
	return false, nil
}
