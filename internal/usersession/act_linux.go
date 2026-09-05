//go:build linux

package usersession

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// loginctlTimeout bounds every call. loginctl talks to logind over D-Bus, and
// a wedged or busy bus must not hold the agent's sample loop open.
const loginctlTimeout = 5 * time.Second

// idleFor asks logind how long a session has been idle.
//
// The session files this package otherwise parses do not carry the idle
// hint -- logind keeps it in memory and answers for it over D-Bus -- so this
// is the one place that shells out. `show-session -p X --value` is logind's
// scripting interface and prints the raw property, which is a different thing
// from the objection recorded on list(): what was refused there was parsing a
// COLUMN LAYOUT (`list-sessions`) and a human-rendered Timestamp. IdleSinceHint
// is microseconds since the epoch, machine-readable by contract.
//
// IdleHint is read as well as IdleSinceHint, and it decides: IdleSinceHint
// keeps its last value after a user comes back, so trusting the timestamp
// alone would report a session that is being typed into as hours idle.
//
// Everything unknown returns false. A desktop environment that never tells
// logind it is idle -- which is common, and is why this is a hint and not a
// measurement -- reports IdleHint=no forever, and the honest result of that
// is "never idle", not "idle since the epoch".
func idleFor(key string) (time.Duration, bool) {
	hint, ok := showSession(key, "IdleHint")
	if !ok || hint != "yes" {
		// Not idle, or logind will not say. Either way there is nothing to
		// act on; "no" is a real answer and yields a zero duration.
		return 0, ok && hint == "no"
	}
	raw, ok := showSession(key, "IdleSinceHint")
	if !ok {
		return 0, false
	}
	usec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || usec <= 0 {
		return 0, false
	}
	since := time.Unix(usec/1e6, (usec%1e6)*1000)
	d := time.Since(since)
	if d < 0 {
		return 0, true
	}
	return d, true
}

// showSession reads one logind session property.
func showSession(key, prop string) (string, bool) {
	if !validSessionID(key) {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginctlTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "loginctl", "show-session", key, "-p", prop, "--value").Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// logoff ends a session through logind, which runs the session's own
// KillUserProcesses/stop path rather than signalling processes directly.
func logoff(key string) error {
	if !validSessionID(key) {
		return fmt.Errorf("usersession: refusing to act on session id %q", key)
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginctlTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "loginctl", "terminate-session", key).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "No session") {
			// Already gone between the sample and here: the wanted outcome.
			return nil
		}
		if msg == "" {
			return fmt.Errorf("loginctl terminate-session %s: %w", key, err)
		}
		return fmt.Errorf("loginctl terminate-session %s: %w: %s", key, err, msg)
	}
	return nil
}

// notify writes a message to a session's terminal. It reports false when the
// session has no terminal to write to, which is every graphical session --
// reaching a desktop's notification daemon would mean joining the user's own
// D-Bus session, a dependency this agent deliberately does not carry (design
// 0014 section 5 records that gap rather than papering over it).
func notify(key, message string) bool {
	tty, ok := sessionKey(key, "TTY")
	if !ok || tty == "" {
		return false
	}
	// logind stores the bare device name; anything with a path separator in
	// it did not come from logind and is not being opened.
	if strings.ContainsAny(tty, "/\\") || tty == "." || tty == ".." {
		return false
	}
	f, err := openTTY(filepath.Join("/dev", tty))
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\r\n%s\r\n", message)
	return err == nil
}

// validSessionID keeps a session id from ever becoming an argument that means
// something else. logind ids are short alphanumerics; anything else is either
// a bug in the collector or something that should not reach a command line.
func validSessionID(key string) bool {
	if key == "" || len(key) > 32 {
		return false
	}
	for _, r := range key {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			continue
		}
		return false
	}
	return true
}
