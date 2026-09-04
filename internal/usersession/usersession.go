// Package usersession reports who is logged on to the host (design 0008).
// These are observations riding the poll request, never desired state: the
// server is told which sessions are open and which the agent watched end.
//
// It is deliberately sessions and not login/logout events. An event log
// cannot record the logout of a machine that lost power -- the lab server's
// legacy `userTracking` rows show six of eleven sessions with no logout at
// all -- so the open set is re-reported and the server closes what vanished.
package usersession

import (
	"sort"
	"strings"
	"time"
)

// Session is one logon. Key is the OS session identifier, unique on the host
// while the session lives; it is what makes a second logon by the same user a
// distinct session rather than an ambiguous second event.
//
// User and Domain are kept separate and unmangled. FOG's legacy client
// lowercased the name and stripped the domain before sending it, which merges
// CORP\jsmith and LAB\jsmith into one person (design 0008 section 1).
type Session struct {
	Key    string `json:"key"`
	User   string `json:"user"`
	Domain string `json:"domain,omitempty"`
	// SID is the Windows security identifier or the Linux uid. A username can
	// be renamed; this survives it.
	SID string `json:"sid,omitempty"`
	// Type is console, remote, tty, x11, wayland or unknown. Console versus
	// remote is the distinction the legacy table could not express.
	Type string `json:"type"`
	// State is active, disconnected or locked. A disconnected RDP session is
	// still a logged-in user holding a profile.
	State      string `json:"state"`
	RemoteHost string `json:"remote_host,omitempty"`
	// StartedAt is the OS-reported logon time, so it is exact regardless of
	// when the agent sampled or polled.
	StartedAt time.Time `json:"started_at"`
	// EndedAt and EndReason are set only on a session the agent watched end.
	// A session that ends because the machine died is closed server-side and
	// marked inferred there; the agent never invents an end time.
	EndedAt   time.Time `json:"ended_at,omitzero"`
	EndReason string    `json:"end_reason,omitempty"`
}

// Report is the poll-request block: what is open now, and what closed since
// the server last acknowledged.
type Report struct {
	Open   []Session `json:"open"`
	Closed []Session `json:"closed,omitempty"`
}

// End reasons the agent itself can observe. "inferred" is deliberately not
// here: only the server sets that, when a host stops reporting a session it
// never saw close.
const (
	EndLogout       = "logout"
	EndDisconnect   = "disconnect"
	EndServiceStop  = "service_stop"
	StateActive     = "active"
	StateDisconnect = "disconnected"
	StateLocked     = "locked"
	TypeConsole     = "console"
	TypeRemote      = "remote"
	TypeUnknown     = "unknown"
)

// List reads the host's current sessions via the OS-specific collector. The
// bool is false when this platform has no collector: the caller must then
// send no session block at all, because an empty open set would be read as
// "nobody is logged in" and close every real session on the server.
//
// An empty slice with true is different and legitimate -- it means the
// collector ran and genuinely found nobody logged on.
func List() ([]Session, bool) { return list() }

// Closed returns the sessions present in prev but gone from cur, stamped with
// the time the agent noticed. The reason distinguishes a session that ended
// from one that merely went away while still disconnected, which is why prev
// carries State rather than just a key set.
//
// Untagged and pure so it is testable on any platform.
func Closed(prev, cur []Session, at time.Time) []Session {
	live := make(map[string]struct{}, len(cur))
	for _, s := range cur {
		live[s.Key] = struct{}{}
	}
	var out []Session
	for _, s := range prev {
		if _, ok := live[s.Key]; ok {
			continue
		}
		s.EndedAt = at
		if s.State == StateDisconnect {
			s.EndReason = EndDisconnect
		} else {
			s.EndReason = EndLogout
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Digest is a stable summary of the open set, stored so the agent can tell
// whether the set has moved since the server last acknowledged one.
//
// It covers the fields the server stores and would need to correct -- state,
// remote host and the identity -- but NOT a timestamp, so an unchanged set
// does not resend every poll. Sessions are sorted first: the OS enumeration
// order is not guaranteed stable, and an order-sensitive digest would report
// a change on every poll and defeat the whole gate.
func Digest(open []Session) string {
	keys := make([]string, 0, len(open))
	for _, s := range open {
		keys = append(keys, strings.Join([]string{
			s.Key, s.User, s.Domain, s.SID, s.Type, s.State, s.RemoteHost,
			s.StartedAt.UTC().Format(time.RFC3339),
		}, "\x1f"))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x1e")
}

// normalizeType maps a collector's raw session type to the vocabulary design
// 0008 fixes for the husType column. An unrecognized value becomes "unknown"
// rather than being passed through, so the column stays queryable.
func normalizeType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "console", "seat0":
		return TypeConsole
	case "remote", "rdp":
		return TypeRemote
	case "tty":
		return "tty"
	case "x11":
		return "x11"
	case "wayland":
		return "wayland"
	case "":
		return TypeUnknown
	}
	return TypeUnknown
}

// stationType maps a Windows window-station name to design 0008's vocabulary.
// "Console" is the physical machine; "RDP-Tcp#3" and friends are remote.
//
// Untagged, next to normalizeType, so it is testable off Windows -- the same
// reason internal/inventory keeps physicalAdapter out of its windows file.
func stationType(station string) string {
	switch {
	case station == "":
		return TypeUnknown
	case strings.HasPrefix(station, "Console"):
		return TypeConsole
	case strings.HasPrefix(station, "RDP"):
		return TypeRemote
	}
	return normalizeType(station)
}
