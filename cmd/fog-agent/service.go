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
	logName = "fog-agent.log"
	// logKeep is the size past which the log rolls to fog-agent.log.1.
	// The agent writes one line per change of state, so this is months.
	logKeep = 1 << 20
)

// logPath is where the service log lives: beside the state directory,
// not inside it. The state directory is locked to SYSTEM and
// Administrators because it holds the key, and the log has to be readable
// by whoever is asked to post it on the forums. With the default state
// directory that is C:\ProgramData\FOG\fog-agent.log, the successor to
// the legacy client's C:\fog.log.
func logPath(dir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(dir)), logName)
}

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

// openLog opens the log for the state directory dir for appending,
// rolling it to fog-agent.log.1 first when it has grown past logKeep.
func openLog(dir string) (*os.File, error) {
	path := logPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > logKeep {
		os.Remove(path + ".1")
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
