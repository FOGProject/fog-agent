package main

import (
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

func TestUnauthorizedGraceStartsTheClockAndHoldsTheIdentity(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	now := time.Now()

	drop, err := unauthorizedTooLong(st, now)
	if err != nil {
		t.Fatal(err)
	}
	if drop {
		t.Error("the first refusal must never drop the certificate; that is the whole defect")
	}
	if st.Config.UnauthorizedSince.IsZero() {
		t.Error("the start of the run must be recorded, or the grace period can never elapse")
	}

	// It must survive a restart, or a crash-looping agent resets the clock
	// forever and the backstop never fires.
	reloaded, err := enroll.Load(st.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Config.UnauthorizedSince.Equal(st.Config.UnauthorizedSince) {
		t.Errorf("UnauthorizedSince did not persist: %v vs %v",
			reloaded.Config.UnauthorizedSince, st.Config.UnauthorizedSince)
	}
}

func TestUnauthorizedGraceDropsOnlyAfterTheWindow(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		since time.Duration
		want  bool
	}{
		{"a minute in", time.Minute, false},
		{"a long weekend", 72 * time.Hour, false},
		{"one second short", UnauthorizedGrace - time.Second, false},
		{"exactly the window", UnauthorizedGrace, true},
		{"well past", 30 * 24 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &enroll.State{Dir: t.TempDir()}
			st.Config.UnauthorizedSince = now.Add(-tc.since)
			drop, err := unauthorizedTooLong(st, now)
			if err != nil {
				t.Fatal(err)
			}
			if drop != tc.want {
				t.Errorf("after %s: drop = %t, want %t", tc.since, drop, tc.want)
			}
		})
	}
}

// An outage that recovers must not leave credit toward a later one.
func TestClearUnauthorizedResetsTheRun(t *testing.T) {
	st := &enroll.State{Dir: t.TempDir()}
	st.Config.UnauthorizedSince = time.Now().Add(-6 * 24 * time.Hour)
	if err := clearUnauthorized(st); err != nil {
		t.Fatal(err)
	}
	if !st.Config.UnauthorizedSince.IsZero() {
		t.Fatal("a successful poll must clear the run")
	}
	reloaded, err := enroll.Load(st.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Config.UnauthorizedSince.IsZero() {
		t.Error("the reset must be persisted, or a restart resurrects six days of refusals")
	}
	// A fresh run then starts from now, not from the old start.
	now := time.Now()
	if drop, err := unauthorizedTooLong(reloaded, now); err != nil || drop {
		t.Errorf("a new run must start clean: drop = %t, err = %v", drop, err)
	}
}
