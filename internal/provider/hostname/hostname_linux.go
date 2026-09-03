//go:build linux

package hostname

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func osCurrent() (string, error) {
	return os.Hostname()
}

// osSet prefers hostnamectl, which writes /etc/hostname, tells the kernel
// and notifies everything listening on hostnamed; where there is no
// systemd it does the first two by hand. No reboot either way.
func osSet(name string) (bool, error) {
	if path, err := exec.LookPath("hostnamectl"); err == nil {
		out, err := exec.Command(path, "set-hostname", name).CombinedOutput()
		if err != nil {
			return false, errors.New(strings.TrimSpace(string(out)))
		}
		return false, nil
	}
	if err := os.WriteFile("/etc/hostname", []byte(name+"\n"), 0o644); err != nil {
		return false, err
	}
	return false, syscall.Sethostname([]byte(name))
}
