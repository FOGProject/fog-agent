package power

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseRejects(t *testing.T) {
	for _, e := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "a * * * *", "5-1 * * *  *", "*/0 * * * *", "5/10 * * * *"} {
		if _, err := Parse(e); err == nil {
			t.Errorf("%q: expected an error", e)
		}
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		expr string
		when string
		want bool
	}{
		{"30 22 * * *", "2026-09-03 22:30", true},
		{"30 22 * * *", "2026-09-03 22:31", false},
		{"*/15 * * * *", "2026-09-03 22:45", true},
		{"*/15 * * * *", "2026-09-03 22:50", false},
		{"0 8-17 * * 1-5", "2026-09-03 09:00", true},  // a Thursday
		{"0 8-17 * * 1-5", "2026-09-05 09:00", false}, // Saturday
		{"0 8 * * 7", "2026-09-06 08:00", true},       // Sunday as 7
		{"0 8 * * 0", "2026-09-06 08:00", true},       // Sunday as 0
		{"0 0 1 * *", "2026-10-01 00:00", true},
		{"0 0 1 * *", "2026-10-02 00:00", false},
		{"0 0 15 * 1", "2026-09-15 00:00", true}, // dom matches, dow (Tuesday) does not: either is enough
		{"0 0 15 * 1", "2026-09-14 00:00", true}, // Monday, dom 14 does not: either is enough
		{"0 0 15 * 1", "2026-09-16 00:00", false},
		{"0 0 * 9 *", "2026-09-16 00:00", true},
		{"0 0 * 10 *", "2026-09-16 00:00", false},
		{"0,30 12 * * *", "2026-09-16 12:30", true},
		{"0,30 12 * * *", "2026-09-16 12:15", false},
	}
	for _, c := range cases {
		cr, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("%q: %v", c.expr, err)
		}
		if got := cr.Matches(at(c.when)); got != c.want {
			t.Errorf("%q at %s: got %v, want %v", c.expr, c.when, got, c.want)
		}
	}
}

func TestNext(t *testing.T) {
	cases := []struct {
		expr, from, want string
	}{
		{"30 22 * * *", "2026-09-03 22:30", "2026-09-04 22:30"}, // strictly after
		{"30 22 * * *", "2026-09-03 22:29", "2026-09-03 22:30"},
		{"0 8 * * 1-5", "2026-09-04 09:00", "2026-09-07 08:00"}, // Friday after 8 -> Monday
		{"0 0 1 1 *", "2026-09-04 09:00", "2027-01-01 00:00"},
		{"*/20 * * * *", "2026-09-04 09:05", "2026-09-04 09:20"},
	}
	for _, c := range cases {
		cr, _ := Parse(c.expr)
		got, ok := cr.Next(at(c.from))
		if !ok || !got.Equal(at(c.want)) {
			t.Errorf("%q from %s: got %v %v, want %s", c.expr, c.from, got, ok, c.want)
		}
	}
	cr, _ := Parse("0 0 30 2 *")
	if _, ok := cr.Next(at("2026-09-04 09:00")); ok {
		t.Error("February 30th must never fire")
	}
}

func TestNextAcrossSchedules(t *testing.T) {
	s := []Schedule{
		{Cron: "0 23 * * *", Action: ActionShutdown},
		{Cron: "bad", Action: ActionReboot},
		{Cron: "30 22 * * *", Action: ActionReboot},
	}
	when, which, ok := Next(s, at("2026-09-03 22:00"))
	if !ok || !when.Equal(at("2026-09-03 22:30")) || which.Action != ActionReboot {
		t.Errorf("got %v %+v %v", when, which, ok)
	}
	if _, _, ok := Next(nil, at("2026-09-03 22:00")); ok {
		t.Error("no schedules, no firing")
	}
}
