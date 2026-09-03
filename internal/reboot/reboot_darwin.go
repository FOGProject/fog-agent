//go:build darwin

package reboot

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func osLoggedIn() (int, error) {
	out, err := exec.Command("who").Output()
	if err != nil {
		return 0, err
	}
	users := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) > 0 {
			users[f[0]] = true
		}
	}
	return len(users), nil
}

func osReboot(delay time.Duration, message string) error {
	when := "now"
	if delay > 0 {
		when = fmt.Sprintf("+%d", int((delay+time.Minute-1)/time.Minute))
	}
	out, err := exec.Command("shutdown", "-r", when, message).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}
