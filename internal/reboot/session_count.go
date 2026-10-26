package reboot

import (
	"errors"
	"fmt"
	"unicode/utf16"
)

// session is the portable subset of WTS_SESSION_INFOW needed by the
// interactive-session counter.
type session struct {
	id    uint32
	state uint32
}

const (
	sessionActive       uint32 = 0
	sessionDisconnected uint32 = 4
	serviceSessionID    uint32 = 0
)

var errMalformedWTSUserName = errors.New("malformed WTS user name response")

// decodeWTSUserName validates the byte-bounded UTF-16 response before
// decoding it. WTS returns a NUL-terminated string, including for an empty
// user name.
func decodeWTSUserName(units []uint16, byteCount uint32) (string, error) {
	if byteCount < 2 || byteCount%2 != 0 || uint64(byteCount/2) != uint64(len(units)) || units[len(units)-1] != 0 {
		return "", errMalformedWTSUserName
	}
	for _, unit := range units[:len(units)-1] {
		if unit == 0 {
			return "", errMalformedWTSUserName
		}
	}
	return string(utf16.Decode(units[:len(units)-1])), nil
}

// countLoggedInSessions counts active or disconnected non-service sessions
// only when WTS reports a nonempty user name. username is called only for
// those candidate sessions; failures must abort the reboot decision.
func countLoggedInSessions(sessions []session, username func(uint32) (string, error)) (int, error) {
	n := 0
	for _, s := range sessions {
		if s.id == serviceSessionID || (s.state != sessionActive && s.state != sessionDisconnected) {
			continue
		}
		name, err := username(s.id)
		if err != nil {
			return 0, fmt.Errorf("query user name for session %d: %w", s.id, err)
		}
		if name != "" {
			n++
		}
	}
	return n, nil
}
