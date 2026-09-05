//go:build linux

package usersession

import (
	"os"
	"path/filepath"
	"strings"
)

// sessionKey reads one raw key out of a logind session file. The parser in
// list_linux.go builds a whole Session; this is for the few keys that are not
// part of that struct, such as TTY.
func sessionKey(id, key string) (string, bool) {
	if !validSessionID(id) {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(logindDir, id))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && k == key {
			return v, true
		}
	}
	return "", false
}

// openTTY opens a terminal for writing. Split out so notify() has no direct
// os.OpenFile call to be mistaken for a general file write.
func openTTY(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY, 0)
}
