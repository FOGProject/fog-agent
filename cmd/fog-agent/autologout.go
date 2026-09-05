package main

import (
	"fmt"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/provider/autologout"
	"github.com/FOGProject/fog-agent/internal/usersession"
)

// autoLogout warns and logs off idle sessions, once per sample.
//
// It is deliberately silent when the policy is off, when no session is idle
// enough, or when the OS will not say how idle a session is. That last case
// is the one worth naming: a platform or a Windows build that does not
// populate a session's last-input time reports "unknown", and unknown does
// nothing at all. Reading it as "idle forever" would log a whole fleet off
// on the agent's first sample, which is the single worst thing this
// capability could do.
func (w *sessionWatcher) autoLogout(st *enroll.State, now time.Time, out *sayer) {
	policy := st.Config.AutoLogout
	if w.warned == nil {
		w.warned = map[string]bool{}
	}
	if !policy.Enabled() {
		// Plan clears the warning set for us, so an operator turning the
		// policy off does not leave anyone half-warned.
		autologout.Plan(policy, nil, w.warned, now)
		return
	}

	idle := make([]autologout.Idle, 0, len(w.open))
	for _, s := range w.open {
		// A locked session is still a user holding a profile, and the
		// legacy module logged it off too -- locking the screen and walking
		// away is precisely the case this exists for.
		d, ok := usersession.IdleFor(s.Key)
		if !ok {
			continue
		}
		idle = append(idle, autologout.Idle{Key: s.Key, User: s.User, For: d})
	}
	if len(idle) == 0 {
		return
	}

	for _, a := range autologout.Plan(policy, idle, w.warned, now) {
		switch a.Kind {
		case autologout.ActWarn:
			msg := fmt.Sprintf("%s (%s)", a.Message, a.In.Round(time.Second))
			if !usersession.Notify(a.Key, msg) {
				// Undeliverable: say so once, and do not un-warn. The
				// logoff still happens -- a policy that silently stopped
				// working because the warning could not be shown would be
				// worse than one that acts without a warning it cannot give.
				out.say("autologout: cannot warn " + a.User + " (session " + a.Key + " has no way to show a message)")
			}
		case autologout.ActLogoff:
			if err := usersession.Logoff(a.Key); err != nil {
				out.say("autologout: " + a.User + ": " + err.Error())
				continue
			}
			out.say(fmt.Sprintf("autologout: logged off %s after %d minutes idle", a.User, policy.Minutes))
		}
	}
}
