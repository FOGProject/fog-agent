package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// The service's log: what the Windows service writes instead of stderr.
// Kept out of the Windows-only file so the tests run everywhere.
const (
	logName = "agent.log"
	// logKeep is the size past which the log rolls to agent.log.1. The
	// agent writes one line per change of state, so this is months.
	logKeep = 1 << 20
)

// dirArg is the --dir the service was registered with, or the default.
func dirArg(args []string) string {
	for i, a := range args {
		if a == "--dir" || a == "-dir" {
			if i+1 < len(args) {
				return args[i+1]
			}
		} else if v, ok := strings.CutPrefix(a, "--dir="); ok {
			return v
		}
	}
	return enroll.DefaultDir
}

// openLog opens dir\agent.log for appending, rolling it to agent.log.1
// first when it has grown past logKeep.
func openLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, logName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > logKeep {
		os.Remove(path + ".1")
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
