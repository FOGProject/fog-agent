package usersession

import (
	"testing"
	"time"
)

func sess(key, user, state string) Session {
	return Session{Key: key, User: user, State: state, Type: TypeConsole,
		StartedAt: time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)}
}

// A session that disappears is closed, and the reason distinguishes a user
// who logged off from an RDP session that was already disconnected. Getting
// this wrong would file every dropped RDP session as a deliberate logout.
func TestClosedStampsWhatVanished(t *testing.T) {
	at := time.Date(2026, 9, 4, 9, 30, 0, 0, time.UTC)
	prev := []Session{
		sess("2", "telliott", StateActive),
		sess("3", "tom", StateDisconnect),
		sess("4", "stillhere", StateActive),
	}
	cur := []Session{sess("4", "stillhere", StateActive)}

	got := Closed(prev, cur, at)
	if len(got) != 2 {
		t.Fatalf("closed %d sessions, want 2: %+v", len(got), got)
	}
	if got[0].Key != "2" || got[1].Key != "3" {
		t.Errorf("not sorted by key: %s, %s", got[0].Key, got[1].Key)
	}
	if got[0].EndReason != EndLogout {
		t.Errorf("active session closed as %q, want %q", got[0].EndReason, EndLogout)
	}
	if got[1].EndReason != EndDisconnect {
		t.Errorf("disconnected session closed as %q, want %q", got[1].EndReason, EndDisconnect)
	}
	for _, s := range got {
		if !s.EndedAt.Equal(at) {
			t.Errorf("session %s ended at %v, want %v", s.Key, s.EndedAt, at)
		}
	}
}

// Nothing vanished means nothing closed -- the case that runs on almost every
// sample, and the one that would spam the server with duplicate closures.
func TestClosedIsEmptyWhenNothingWent(t *testing.T) {
	cur := []Session{sess("2", "telliott", StateActive)}
	if got := Closed(cur, cur, time.Now()); len(got) != 0 {
		t.Errorf("closed %+v from an unchanged set, want none", got)
	}
}

// The digest must not depend on enumeration order. If it did, an OS that
// returned sessions in a different order between two samples would look like
// a change every time and defeat the send gate entirely.
func TestDigestIgnoresOrder(t *testing.T) {
	a := []Session{sess("2", "telliott", StateActive), sess("3", "tom", StateActive)}
	b := []Session{sess("3", "tom", StateActive), sess("2", "telliott", StateActive)}
	if Digest(a) != Digest(b) {
		t.Error("digest changed when only the order did")
	}
}

// ...but it must move on the things the server stores, or a state change
// would never be sent and the server's row would stay wrong forever.
func TestDigestMovesOnStoredFields(t *testing.T) {
	base := []Session{sess("2", "telliott", StateActive)}
	for _, tc := range []struct {
		name string
		mod  func(*Session)
	}{
		{"state", func(s *Session) { s.State = StateDisconnect }},
		{"user", func(s *Session) { s.User = "someone-else" }},
		{"remote host", func(s *Session) { s.RemoteHost = "10.0.0.9" }},
		{"type", func(s *Session) { s.Type = TypeRemote }},
		{"start", func(s *Session) { s.StartedAt = s.StartedAt.Add(time.Hour) }},
		{"sid", func(s *Session) { s.SID = "S-1-5-21-1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := []Session{base[0]}
			tc.mod(&changed[0])
			if Digest(base) == Digest(changed) {
				t.Errorf("digest unchanged after %s moved", tc.name)
			}
		})
	}
}

// An empty set has to have a stable digest of its own: it is the normal state
// of an unattended machine, and it must compare equal to itself so the agent
// does not resend "nobody is logged in" on every poll.
func TestDigestOfEmptySetIsStable(t *testing.T) {
	if Digest(nil) != Digest([]Session{}) {
		t.Error("nil and empty open sets digest differently")
	}
}

func TestStationType(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Console", TypeConsole},
		{"RDP-Tcp#3", TypeRemote},
		{"RDP-Tcp", TypeRemote},
		{"", TypeUnknown},
		{"Something-New", TypeUnknown},
	} {
		if got := stationType(tc.in); got != tc.want {
			t.Errorf("stationType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeTypeKeepsVocabularyClosed(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"tty", "tty"},
		{"x11", "x11"},
		{"wayland", "wayland"},
		{"  TTY  ", "tty"},
		{"", TypeUnknown},
		{"something-systemd-adds-in-2030", TypeUnknown},
	} {
		if got := normalizeType(tc.in); got != tc.want {
			t.Errorf("normalizeType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
