package main

import (
	"context"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/usersession"
)

// SessionSampleInterval is how often the agent re-reads the host's sessions
// between polls. It sets the accuracy of an observed session end: a logoff is
// noticed within this long, rather than within a poll interval (five minutes
// by default, and up to whatever an admin sets).
//
// Thirty seconds is a compromise, not a measurement. Reading logind's session
// files or calling WTSEnumerateSessions is cheap, but it is not free, and a
// user-visible session end does not need second precision. Design 0008 notes
// the upgrade that removes the compromise entirely: both platforms can push
// an event (SERVICE_CONTROL_SESSIONCHANGE, logind's D-Bus signals) instead of
// being asked.
const SessionSampleInterval = 30 * time.Second

// SessionResyncInterval is how often the open set is resent even when it has
// not changed, so a server-side row that drifted is corrected.
const SessionResyncInterval = time.Hour

// sessionWatcher holds the last sampled open set.
//
// Deliberately in memory and NOT persisted. On a fresh start the agent knows
// only what is open now, and the sessions that ended while it was down have
// no end time it could honestly supply -- stamping them with the startup time
// would invent a duration. The server closes those as `inferred` from the
// host's last contact, which is the truthful answer. See design 0008 §2.
type sessionWatcher struct {
	open []usersession.Session
}

// sample re-reads the host's sessions and records any that vanished since the
// last look. Closures accumulate in Config.SessionsPending until a poll
// carries them and the server answers 200.
//
// Does nothing when the server has said collect_sessions is false: the gate
// stops the agent reading who is logged on, not merely sending it.
func (w *sessionWatcher) sample(st *enroll.State, now time.Time, out *sayer) {
	if st.Config.SessionsDisabled {
		return
	}
	cur, ok := usersession.List()
	if !ok {
		// No collector on this platform. Leave w.open alone: treating this
		// as an empty set would manufacture a closure for every open session.
		return
	}
	closed := usersession.Closed(w.open, cur, now)
	w.open = cur
	if len(closed) == 0 {
		return
	}
	st.Config.SessionsPending = append(st.Config.SessionsPending, closed...)
	for _, s := range closed {
		out.say("session ended: " + s.User + " (" + s.Type + ", " + s.EndReason + ")")
	}
	if err := st.SaveConfig(); err != nil {
		out.say("state: " + err.Error())
	}
}

// attach hangs the session block on a poll request when there is something to
// say: the open set differs from the one the server acknowledged, there are
// unacknowledged closures, or the resync interval has passed.
//
// Returns the digest that this request is claiming, so recordSessions can
// store it only after the poll succeeds.
func (w *sessionWatcher) attach(st *enroll.State, req *enroll.PollRequest, now time.Time) (string, bool) {
	if st.Config.SessionsDisabled {
		return "", false
	}
	// Sample once here too, so a poll always carries the current truth even
	// if it arrives before the first tick of the sampler.
	if cur, ok := usersession.List(); ok {
		w.open = cur
	} else if len(w.open) == 0 {
		// Never had a collector: say nothing rather than claim nobody is on.
		return "", false
	}

	digest := usersession.Digest(w.open)
	stale := now.Sub(st.Config.SessionsChecked) >= SessionResyncInterval
	if digest == st.Config.SessionsAcked && len(st.Config.SessionsPending) == 0 && !stale {
		return "", false
	}
	req.Sessions = &usersession.Report{
		Open:   w.open,
		Closed: st.Config.SessionsPending,
	}
	return digest, true
}

// recordSessions stores what the server accepted. Called only after a poll
// returns 200: the pending closures are dropped here and nowhere else, so a
// failed poll resends them instead of losing a session end forever.
func recordSessions(st *enroll.State, digest string, sent bool, resp *enroll.PollResponse, now time.Time) error {
	changed := false

	if resp.CollectSessions != nil && st.Config.SessionsDisabled == *resp.CollectSessions {
		st.Config.SessionsDisabled = !*resp.CollectSessions
		changed = true
	}
	if st.Config.SessionsDisabled {
		// Turned off: drop anything queued rather than holding a report the
		// server has said it does not want.
		if len(st.Config.SessionsPending) > 0 || st.Config.SessionsAcked != "" {
			st.Config.SessionsPending = nil
			st.Config.SessionsAcked = ""
			changed = true
		}
	} else if sent {
		st.Config.SessionsPending = nil
		st.Config.SessionsAcked = digest
		st.Config.SessionsChecked = now
		changed = true
	}

	if !changed {
		return nil
	}
	return st.SaveConfig()
}

// sleepSampling waits for d, re-reading the host's sessions every
// SessionSampleInterval while it does.
//
// The poll loop used to sleep the whole interval in one time.After. Sessions
// need a finer look than that: a five-minute poll would put a five-minute
// error on every logoff time, and an admin can set the poll interval far
// higher. The wait still ends exactly when it was going to.
func sleepSampling(ctx context.Context, st *enroll.State, watch *sessionWatcher, d time.Duration, out *sayer) error {
	deadline := time.Now().Add(d)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		step := SessionSampleInterval
		if remaining < step {
			step = remaining
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(step):
			watch.sample(st, time.Now(), out)
		}
	}
}
