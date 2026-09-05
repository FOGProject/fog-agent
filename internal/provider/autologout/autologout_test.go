package autologout

import (
	"testing"
	"time"
)

var now = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func idle(key string, d time.Duration) Idle {
	return Idle{Key: key, User: "telliott", For: d}
}

func kinds(as []Action) map[string]string {
	m := map[string]string{}
	for _, a := range as {
		m[a.Key] = a.Kind
	}
	return m
}

func TestBelowFloorDoesNothing(t *testing.T) {
	// The legacy module refused anything under five minutes and so does
	// this. A session idle for a week must not be touched by a two-minute
	// policy that should never have been accepted.
	p := Policy{Minutes: 2, WarnSeconds: 30}
	got := Plan(p, []Idle{idle("1", 7*24*time.Hour)}, map[string]bool{}, now)
	if len(got) != 0 {
		t.Fatalf("policy below the floor acted: %+v", got)
	}
}

func TestZeroMinutesIsOff(t *testing.T) {
	got := Plan(Policy{}, []Idle{idle("1", time.Hour)}, map[string]bool{}, now)
	if len(got) != 0 {
		t.Fatalf("disabled policy acted: %+v", got)
	}
}

func TestWarnThenLogoff(t *testing.T) {
	p := Policy{Minutes: 10, WarnSeconds: 60}
	warned := map[string]bool{}

	// Eight minutes: under the warning threshold of nine.
	if got := Plan(p, []Idle{idle("1", 8*time.Minute)}, warned, now); len(got) != 0 {
		t.Fatalf("acted too early: %+v", got)
	}

	// Nine and a half: warn.
	got := Plan(p, []Idle{idle("1", 9*time.Minute+30*time.Second)}, warned, now)
	if k := kinds(got); k["1"] != ActWarn {
		t.Fatalf("want a warning, got %+v", got)
	}
	if got[0].In != 30*time.Second {
		t.Fatalf("warning should say 30s remain, said %s", got[0].In)
	}
	if !warned["1"] {
		t.Fatal("warning was not recorded")
	}

	// Still idle a tick later: do NOT warn twice.
	if got := Plan(p, []Idle{idle("1", 9*time.Minute+45*time.Second)}, warned, now); len(got) != 0 {
		t.Fatalf("warned twice: %+v", got)
	}

	// Past the timeout: log off, and forget the warning.
	got = Plan(p, []Idle{idle("1", 10*time.Minute)}, warned, now)
	if k := kinds(got); k["1"] != ActLogoff {
		t.Fatalf("want a logoff, got %+v", got)
	}
	if warned["1"] {
		t.Fatal("warning record survived the logoff")
	}
}

func TestUserComesBackAndIsWarnedAgain(t *testing.T) {
	// The case a set that only ever grew would get wrong: warned once,
	// came back, went away again. The second warning has to happen.
	p := Policy{Minutes: 10, WarnSeconds: 60}
	warned := map[string]bool{}

	Plan(p, []Idle{idle("1", 9*time.Minute+30*time.Second)}, warned, now)
	if !warned["1"] {
		t.Fatal("first warning not recorded")
	}

	// Mouse moved: idle drops back to nothing.
	if got := Plan(p, []Idle{idle("1", 0)}, warned, now); len(got) != 0 {
		t.Fatalf("acted on an active session: %+v", got)
	}
	if warned["1"] {
		t.Fatal("returning user did not clear the warning")
	}

	got := Plan(p, []Idle{idle("1", 9*time.Minute+30*time.Second)}, warned, now)
	if k := kinds(got); k["1"] != ActWarn {
		t.Fatalf("want a second warning, got %+v", got)
	}
}

func TestVanishedSessionLosesItsWarning(t *testing.T) {
	// Session ids are reused. A stale warning record would suppress the
	// warning for whoever gets that id next.
	p := Policy{Minutes: 10, WarnSeconds: 60}
	warned := map[string]bool{"1": true, "2": true}

	Plan(p, []Idle{idle("2", 0)}, warned, now)
	if warned["1"] {
		t.Fatal("warning survived a session that is gone")
	}
}

func TestDisablingClearsWarnings(t *testing.T) {
	warned := map[string]bool{"1": true}
	Plan(Policy{}, []Idle{idle("1", time.Hour)}, warned, now)
	if len(warned) != 0 {
		t.Fatalf("disabled policy kept warnings: %+v", warned)
	}
}

func TestWarningLongerThanTimeoutIsClamped(t *testing.T) {
	// A 10-minute timeout with a 20-minute warning would otherwise fire the
	// instant anybody stopped typing. Clamped to half, so the first warning
	// lands at five minutes idle, not at zero.
	p := Policy{Minutes: 10, WarnSeconds: 20 * 60}
	warned := map[string]bool{}

	if got := Plan(p, []Idle{idle("1", time.Second)}, warned, now); len(got) != 0 {
		t.Fatalf("warned an active session: %+v", got)
	}
	got := Plan(p, []Idle{idle("1", 6*time.Minute)}, warned, now)
	if k := kinds(got); k["1"] != ActWarn {
		t.Fatalf("want a warning at 6 minutes, got %+v", got)
	}
}

func TestNoWarningConfigured(t *testing.T) {
	// WarnSeconds zero is legal: log off with no warning at all.
	p := Policy{Minutes: 10}
	warned := map[string]bool{}

	if got := Plan(p, []Idle{idle("1", 9*time.Minute+59*time.Second)}, warned, now); len(got) != 0 {
		t.Fatalf("acted before the timeout: %+v", got)
	}
	got := Plan(p, []Idle{idle("1", 10*time.Minute)}, warned, now)
	if k := kinds(got); k["1"] != ActLogoff {
		t.Fatalf("want a logoff, got %+v", got)
	}
}

func TestDefaultMessageWhenServerNamesNone(t *testing.T) {
	p := Policy{Minutes: 10, WarnSeconds: 60}
	got := Plan(p, []Idle{idle("1", 9*time.Minute+30*time.Second)}, map[string]bool{}, now)
	if len(got) != 1 || got[0].Message != DefaultMessage {
		t.Fatalf("want the default message, got %+v", got)
	}
}

func TestManySessionsAreOrdered(t *testing.T) {
	// A stable order so the log of what happened is diffable.
	p := Policy{Minutes: 10, WarnSeconds: 60}
	got := Plan(p, []Idle{
		idle("3", 11*time.Minute),
		idle("1", 11*time.Minute),
		idle("2", 11*time.Minute),
	}, map[string]bool{}, now)
	if len(got) != 3 || got[0].Key != "1" || got[1].Key != "2" || got[2].Key != "3" {
		t.Fatalf("unsorted: %+v", got)
	}
}
