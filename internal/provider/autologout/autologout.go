// Package autologout is the auto log out capability (design 0014): log a
// user off after a period of inactivity, so a machine left at a desk does
// not sit holding an open profile and a license seat all night.
//
// It replaces the legacy client's Autologout module, which asked the server
// for `{"time": seconds}` and ran a countdown GUI. This agent is a service
// in session 0 and has no GUI, so the warning goes to the session through
// the OS's own mechanism -- the same choice design 0004 already made for
// reboot warnings -- and the decision of what to do is a pure function over
// per-session idle time, tested off-platform.
package autologout

import (
	"sort"
	"time"
)

// MinMinutes is the shortest idle period the server will act on. The legacy
// module refused anything under five (`FOG\Client\Autologout::json()` returns
// `['error' => 'time']` below it) and the reason is still good: a two-minute
// idle timer logs people off while they read the screen. Kept as a floor on
// the agent as well as the server, because a policy that arrived wrong should
// not be able to log a whole fleet off.
const MinMinutes = 5

// Policy is what the server sends for this capability.
type Policy struct {
	// Minutes of inactivity before a session is logged off. Zero, absent, or
	// anything under MinMinutes disables it -- which is the same "0 disables"
	// contract FOG_CLIENT_AUTOLOGOFF_MIN has always had.
	Minutes int `json:"minutes"`
	// WarnSeconds is how long before the logoff the user is told. Zero means
	// no warning at all, which is legal but hostile; the server defaults it.
	WarnSeconds int `json:"warn_seconds,omitempty"`
	// Message is the text shown in that warning. Empty falls back to
	// DefaultMessage so a session is never logged off with a blank box.
	Message string `json:"message,omitempty"`
}

// DefaultMessage is used when the server names none.
const DefaultMessage = "You are about to be logged out for inactivity. Move the mouse or press a key to stay logged in."

// Enabled says whether this policy asks for anything.
func (p Policy) Enabled() bool { return p.Minutes >= MinMinutes }

// Idle is one session's inactivity, as the OS reports it. Key matches
// usersession.Session.Key so the two can be joined without a second
// enumeration.
type Idle struct {
	Key  string
	User string
	For  time.Duration
}

// Action is what to do about one session on this tick.
type Action struct {
	Key  string
	User string
	// Kind is ActWarn or ActLogoff.
	Kind string
	// Message is set on ActWarn; the countdown the user is shown.
	Message string
	// In is how long until the logoff, on an ActWarn. Reported so the
	// warning can say a real number rather than the policy's nominal one:
	// the sampler ticks on its own schedule, not on the policy's.
	In time.Duration
}

// Action kinds.
const (
	ActWarn   = "warn"
	ActLogoff = "logoff"
)

// Plan decides what to do about every idle session on one sample.
//
// warned is the set of session keys already warned, and Plan rewrites it in
// place: a key is added when a warning is issued and removed as soon as the
// session drops back under the warning threshold. That removal is the whole
// point of keeping the set rather than a timestamp -- a user who comes back,
// leaves again, and is warned a second time is the normal case, and a set
// that only ever grew would warn them once and then log them off silently
// for the rest of the uptime.
//
// It is a pure function over its inputs so the interesting cases (a user
// returning during the warning, a policy below the floor, a session that
// vanished between enumeration and decision) are tested on any platform.
func Plan(p Policy, idle []Idle, warned map[string]bool, now time.Time) []Action {
	if !p.Enabled() {
		// Disabled: forget every warning too, so re-enabling the policy
		// starts everyone from a clean slate rather than logging off
		// whoever happened to be warned when it was turned off.
		for k := range warned {
			delete(warned, k)
		}
		return nil
	}

	limit := time.Duration(p.Minutes) * time.Minute
	warn := time.Duration(p.WarnSeconds) * time.Second
	if warn >= limit {
		// A warning longer than the whole timeout would fire the instant
		// anyone stopped typing. Clamp rather than refuse: the policy still
		// expresses a sane intent, and refusing it would silently disable
		// the capability on a fat-fingered global setting.
		warn = limit / 2
	}
	msg := p.Message
	if msg == "" {
		msg = DefaultMessage
	}

	live := make(map[string]bool, len(idle))
	var out []Action
	for _, s := range idle {
		live[s.Key] = true
		switch {
		case s.For >= limit:
			out = append(out, Action{Key: s.Key, User: s.User, Kind: ActLogoff})
			delete(warned, s.Key)
		case warn > 0 && s.For >= limit-warn:
			if warned[s.Key] {
				continue
			}
			warned[s.Key] = true
			out = append(out, Action{
				Key: s.Key, User: s.User, Kind: ActWarn,
				Message: msg, In: limit - s.For,
			})
		default:
			// Back under the threshold: the user returned.
			delete(warned, s.Key)
		}
	}
	// A session that ended on its own must not keep a warning record; the
	// OS reuses session ids, and a stale entry would suppress the warning
	// for whoever gets that id next.
	for k := range warned {
		if !live[k] {
			delete(warned, k)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
