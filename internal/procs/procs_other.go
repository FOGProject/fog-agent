//go:build !unix

package procs

import "os/exec"

// Attach is a no-op where there are no process groups to attach to: a
// context cancel kills the process itself, and that is all it can do.
func Attach(*exec.Cmd) {}
