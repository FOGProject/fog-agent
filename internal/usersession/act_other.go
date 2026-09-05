//go:build !linux && !windows

package usersession

import (
	"errors"
	"time"
)

// errUnsupported is what a platform with no session-management surface
// reports. Auto log out then does nothing at all here, which is correct: the
// capability cannot warn a user it cannot see.
var errUnsupported = errors.New("usersession: no session control on this platform")

func idleFor(string) (time.Duration, bool) { return 0, false }

func logoff(string) error { return errUnsupported }

func notify(string, string) bool { return false }
