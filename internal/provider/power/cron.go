// Package power is the power capability (design 0004): shutdown and
// reboot schedules the server resolves for the host, fired by the agent's
// own cron matcher through the reboot coordinator, and on-demand actions
// an admin clicked. Wake-on-LAN stays on the server, which sends the
// packet; a running agent has nothing to do for it.
package power

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field expression: minute, hour, day of month,
// month, day of week, the form FOG has always stored per schedule. Each
// field is a bit set over its range.
type Cron struct {
	min, hour, dom, month, dow uint64
	// anyDom and anyDow record a `*`, which decides how the two day
	// fields combine: both restricted means either may match, the way
	// Vixie cron reads it.
	anyDom, anyDow bool
}

type field struct {
	lo, hi int
}

var fields = [5]field{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// Parse reads an expression such as `30 22 * * 1-5`. Each field is a
// comma list of `*`, `*/n`, `a`, `a-b` or `a-b/n`; day of week accepts 7
// as Sunday like 0.
func Parse(expr string) (Cron, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Cron{}, fmt.Errorf("cron %q: want five fields, got %d", expr, len(parts))
	}
	var c Cron
	sets := [5]*uint64{&c.min, &c.hour, &c.dom, &c.month, &c.dow}
	for i, p := range parts {
		bits, any, err := parseField(p, fields[i])
		if err != nil {
			return Cron{}, fmt.Errorf("cron %q: %w", expr, err)
		}
		*sets[i] = bits
		switch i {
		case 2:
			c.anyDom = any
		case 4:
			c.anyDow = any
			if bits&(1<<7) != 0 {
				bits |= 1
				*sets[i] = bits
			}
		}
	}
	return c, nil
}

func parseField(s string, f field) (uint64, bool, error) {
	var bits uint64
	any := false
	for _, item := range strings.Split(s, ",") {
		step := 1
		if i := strings.IndexByte(item, '/'); i >= 0 {
			n, err := strconv.Atoi(item[i+1:])
			if err != nil || n < 1 {
				return 0, false, fmt.Errorf("bad step in %q", item)
			}
			step, item = n, item[:i]
		}
		lo, hi := f.lo, f.hi
		switch {
		case item == "*":
			if step == 1 {
				any = true
			}
		case strings.Contains(item, "-"):
			a, b, _ := strings.Cut(item, "-")
			var err error
			if lo, err = strconv.Atoi(a); err != nil {
				return 0, false, fmt.Errorf("bad range %q", item)
			}
			if hi, err = strconv.Atoi(b); err != nil {
				return 0, false, fmt.Errorf("bad range %q", item)
			}
		default:
			n, err := strconv.Atoi(item)
			if err != nil {
				return 0, false, fmt.Errorf("bad value %q", item)
			}
			lo, hi = n, n
			if step > 1 {
				// `5/10` is not standard; refuse rather than guess.
				return 0, false, fmt.Errorf("step needs a range or * in %q", item)
			}
		}
		if lo < f.lo || hi > f.hi || lo > hi {
			return 0, false, fmt.Errorf("%q outside %d-%d", item, f.lo, f.hi)
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, any, nil
}

// Matches says whether t, to the minute, is a time the expression names.
func (c Cron) Matches(t time.Time) bool {
	return c.min&(1<<uint(t.Minute())) != 0 &&
		c.hour&(1<<uint(t.Hour())) != 0 &&
		c.month&(1<<uint(t.Month())) != 0 &&
		c.dayMatches(t)
}

func (c Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<uint(t.Day())) != 0
	dow := c.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case c.anyDom && c.anyDow:
		return true
	case c.anyDom:
		return dow
	case c.anyDow:
		return dom
	}
	return dom || dow
}

// Next is the first matching minute strictly after t, or false when none
// falls within the coming year (a February 30th, say).
func (c Cron) Next(t time.Time) (time.Time, bool) {
	n := t.Truncate(time.Minute).Add(time.Minute)
	end := n.AddDate(1, 0, 1)
	for n.Before(end) {
		switch {
		case c.month&(1<<uint(n.Month())) == 0:
			n = time.Date(n.Year(), n.Month()+1, 1, 0, 0, 0, 0, n.Location())
		case !c.dayMatches(n):
			n = time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, n.Location())
		case c.hour&(1<<uint(n.Hour())) == 0:
			n = time.Date(n.Year(), n.Month(), n.Day(), n.Hour()+1, 0, 0, 0, n.Location())
		case c.min&(1<<uint(n.Minute())) == 0:
			n = n.Add(time.Minute)
		default:
			return n, true
		}
	}
	return time.Time{}, false
}
