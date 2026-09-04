//go:build linux

package usersession

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// logindDir holds one key=value file per live session. It is a variable so
// the tests can point it at a fixture directory.
var logindDir = "/run/systemd/sessions"

// list reads logind's session files directly.
//
// These files open with "# This is private data. Do not parse." That warning
// is real and this is a deliberate, bounded exception, so the reasoning is
// recorded rather than left for the next reader to rediscover:
//
//   - `loginctl list-sessions` is a display format (column widths, a legend,
//     a "no sessions" footer), so parsing it makes correctness depend on a UI
//     decision in another project.
//   - `loginctl show-session` is the scripting interface, but it renders the
//     session start as a human-readable Timestamp, and design 0008 needs the
//     exact epoch value that REALTIME here already carries.
//   - The supported way to get that is logind's D-Bus API (or libsystemd's
//     sd_session_get_*, which reads these very files). Both cost a dependency
//     -- a D-Bus library or cgo -- that this agent does not otherwise carry,
//     and cgo would break the clean cross-compile the MSI build relies on.
//
// So: parse the files, keep the parser total (any missing or malformed key
// drops that one session instead of emitting a wrong one), and treat a move
// to the D-Bus API as the upgrade if the format ever shifts. The failure mode
// is a session we do not report, never a fabricated one.
//
// The false return is reserved for "no collector here": logind absent. A
// present-but-empty logind is (empty, true), which genuinely means nobody is
// logged on, and the difference matters because the server closes every
// session a host omits.
func list() ([]Session, bool) {
	entries, err := os.ReadDir(logindDir)
	if err != nil {
		// No logind: this build has no other collector, and claiming an empty
		// set here would close every open session on the server.
		return nil, false
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// logind also keeps <id>.ref files; sessions are the bare ids.
		if strings.ContainsRune(e.Name(), '.') {
			continue
		}
		b, err := os.ReadFile(filepath.Join(logindDir, e.Name()))
		if err != nil {
			continue
		}
		if s, ok := parseLogindSession(e.Name(), string(b)); ok {
			out = append(out, s)
		}
	}
	return out, true
}

// parseLogindSession turns one /run/systemd/sessions/<id> file into a
// Session. Untagged logic kept separate from the directory walk so it can be
// tested against captured real files.
//
// Sessions whose CLASS is not "user" are dropped: greeter (the display
// manager's own login screen) and background sessions are not a person being
// logged in, and counting the greeter would report the machine as occupied
// whenever it sits at the login prompt.
func parseLogindSession(id, body string) (Session, bool) {
	kv := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		kv[k] = v
	}
	if kv["USER"] == "" {
		return Session{}, false
	}
	if class := kv["CLASS"]; class != "" && class != "user" {
		return Session{}, false
	}

	s := Session{
		Key:        id,
		User:       kv["USER"],
		SID:        kv["UID"],
		RemoteHost: kv["REMOTE_HOST"],
		StartedAt:  realtimeToTime(kv["REALTIME"]),
	}

	// A remote session's TYPE is still tty/x11, so REMOTE is what separates
	// an ssh login from a console one -- TYPE alone would file every ssh
	// session as a local tty.
	if kv["REMOTE"] == "1" {
		s.Type = TypeRemote
	} else {
		s.Type = normalizeType(kv["TYPE"])
	}

	switch kv["STATE"] {
	case "active":
		s.State = StateActive
	case "online", "closing":
		// online means logged in but not the foreground session -- a switched
		// -away user, which is "still holding a profile", the same thing
		// disconnected means for RDP.
		s.State = StateDisconnect
	default:
		s.State = StateActive
	}
	if kv["LOCKED_HINT"] == "1" {
		s.State = StateLocked
	}
	return s, true
}

// realtimeToTime converts logind's REALTIME (microseconds since the epoch) to
// a time. A missing or unparsable value yields the zero time rather than now:
// a fabricated start would silently become a session duration.
func realtimeToTime(raw string) time.Time {
	usec, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || usec <= 0 {
		return time.Time{}
	}
	return time.Unix(usec/1e6, (usec%1e6)*1000).UTC()
}
