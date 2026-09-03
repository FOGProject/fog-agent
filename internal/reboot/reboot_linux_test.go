//go:build linux

package reboot

import "testing"

// TestCountUserSessionsSkipsTheManager pins the reason presence is counted
// by session class: systemd 256+ books the per-user service manager as a
// session, and it outlives the last real login. On a lab VM that made an
// empty machine read as one user forever.
func TestCountUserSessionsSkipsTheManager(t *testing.T) {
	modern := []byte("55 1000 fog - 3394 manager - no -\n61 1000 fog - 3483 user - no -\n62 1000 fog seat0 3500 greeter tty1 no -\n")
	if n := countUserSessions(modern); n != 1 {
		t.Fatalf("modern loginctl: want 1 user session, got %d", n)
	}
	legacy := []byte("c1 1000 fog seat0 tty2\n  3 1000 fog - pts/0\n")
	if n := countUserSessions(legacy); n != 2 {
		t.Fatalf("legacy loginctl (no class column): want 2, got %d", n)
	}
	if n := countUserSessions(nil); n != 0 {
		t.Fatalf("no sessions: want 0, got %d", n)
	}
}
