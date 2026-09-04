//go:build linux

package usersession

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Captured verbatim from /run/systemd/sessions/110 on the Debian lab VM on
// 2026-09-04 -- a real ssh login, not a format written from memory. The
// leading comment line is systemd's own.
const realSSHSession = `# This is private data. Do not parse.
UID=1000
USER=fog
ACTIVE=1
IS_DISPLAY=1
STATE=active
REMOTE=1
LEADER_FD_SAVED=1
TYPE=tty
ORIGINAL_TYPE=tty
CLASS=user
SCOPE=session-110.scope
FIFO=/run/systemd/sessions/110.ref
REMOTE_HOST=192.168.56.1
SERVICE=sshd
POSITION=0
LEADER=5904
AUDIT=110
REALTIME=1788538381876727
MONOTONIC=13724739161
`

func TestParseRealSSHSession(t *testing.T) {
	got, ok := parseLogindSession("110", realSSHSession)
	if !ok {
		t.Fatal("a real ssh session was rejected")
	}
	// REMOTE=1 must win over TYPE=tty. Filing an ssh login as a local tty is
	// the exact confusion this rule exists to prevent.
	if got.Type != TypeRemote {
		t.Errorf("Type = %q, want %q (REMOTE=1 with TYPE=tty)", got.Type, TypeRemote)
	}
	if got.RemoteHost != "192.168.56.1" {
		t.Errorf("RemoteHost = %q", got.RemoteHost)
	}
	if got.User != "fog" || got.SID != "1000" {
		t.Errorf("User/SID = %q/%q", got.User, got.SID)
	}
	if got.State != StateActive {
		t.Errorf("State = %q", got.State)
	}
	want := time.Unix(1788538381, 876727000).UTC()
	if !got.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want)
	}
}

// The display manager's own greeter is a logind session with a user, but
// nobody is logged in. Counting it would report every idle machine sitting at
// its login prompt as occupied -- which would then block reboots.
func TestParseDropsGreeterAndBackgroundClasses(t *testing.T) {
	for _, class := range []string{"greeter", "background", "manager"} {
		body := "UID=115\nUSER=gdm\nSTATE=online\nTYPE=wayland\nCLASS=" + class + "\nREALTIME=1788538381876727\n"
		if _, ok := parseLogindSession("c1", body); ok {
			t.Errorf("CLASS=%s was accepted as a user session", class)
		}
	}
}

func TestParseSessionStates(t *testing.T) {
	base := "UID=1000\nUSER=fog\nTYPE=x11\nCLASS=user\nREALTIME=1788538381876727\n"
	for _, tc := range []struct{ extra, want string }{
		{"STATE=active\n", StateActive},
		// online means logged in but switched away -- still holding a profile.
		{"STATE=online\n", StateDisconnect},
		{"STATE=closing\n", StateDisconnect},
		{"STATE=active\nLOCKED_HINT=1\n", StateLocked},
	} {
		got, ok := parseLogindSession("s", base+tc.extra)
		if !ok {
			t.Fatalf("rejected: %q", tc.extra)
		}
		if got.State != tc.want {
			t.Errorf("%q -> State %q, want %q", tc.extra, got.State, tc.want)
		}
	}
}

// A session file with no USER is not a session anybody is in. It must be
// dropped rather than reported with an empty username, which the server would
// happily store as a logged-on user called "".
func TestParseRejectsSessionWithoutUser(t *testing.T) {
	if _, ok := parseLogindSession("x", "UID=0\nSTATE=active\nCLASS=user\n"); ok {
		t.Error("accepted a session file with no USER")
	}
}

// A missing or corrupt REALTIME yields the zero time, never time.Now(): a
// fabricated start silently becomes a wrong session duration in a report.
func TestParseBadRealtimeIsZeroNotNow(t *testing.T) {
	for _, rt := range []string{"", "REALTIME=\n", "REALTIME=not-a-number\n", "REALTIME=-5\n"} {
		got, ok := parseLogindSession("s", "UID=1000\nUSER=fog\nCLASS=user\nSTATE=active\n"+rt)
		if !ok {
			t.Fatalf("rejected for %q", rt)
		}
		if !got.StartedAt.IsZero() {
			t.Errorf("REALTIME %q gave StartedAt %v, want zero", rt, got.StartedAt)
		}
	}
}

// The collector must say "no collector" rather than "nobody is logged in"
// when logind is absent, because the server closes every session a host omits
// from its open set.
func TestListWithoutLogindReportsAbsentNotEmpty(t *testing.T) {
	old := logindDir
	logindDir = filepath.Join(t.TempDir(), "does-not-exist")
	defer func() { logindDir = old }()

	got, ok := List()
	if ok {
		t.Errorf("collector claimed to be present without logind (returned %+v)", got)
	}
}

// ...but a logind that is present with no sessions is genuinely empty, and
// must report present-and-empty so the server can close stale rows.
func TestListWithEmptyLogindIsPresentAndEmpty(t *testing.T) {
	old := logindDir
	logindDir = t.TempDir()
	defer func() { logindDir = old }()

	got, ok := List()
	if !ok {
		t.Error("an empty logind directory reported no collector")
	}
	if len(got) != 0 {
		t.Errorf("got %d sessions from an empty directory", len(got))
	}
}

// .ref files sit beside the session files and are not sessions.
func TestListSkipsRefFiles(t *testing.T) {
	dir := t.TempDir()
	old := logindDir
	logindDir = dir
	defer func() { logindDir = old }()

	if err := os.WriteFile(filepath.Join(dir, "110"), []byte(realSSHSession), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "110.ref"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := List()
	if !ok {
		t.Fatal("collector reported absent")
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1 (a .ref file was counted)", len(got))
	}
}
