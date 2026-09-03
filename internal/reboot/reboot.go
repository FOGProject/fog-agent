// Package reboot is the reboot coordinator (design 0001 section 6): the
// only thing in the agent that reboots. Providers and the task poll ask
// for one by recording a reason; the coordinator decides when, from who
// is logged in and the policy the server sent, and it is the one place
// that policy is applied.
package reboot

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SourceTask is the reason source for a FOG task waiting to boot the
// machine into imaging. Every other source is a capability name.
const SourceTask = "task"

// Reason is one outstanding request for a reboot. Force is the policy the
// server attached to it: for a task, FOG_TASK_FORCE_REBOOT; for a
// capability, the host's "Enforce Hostname | AD Join Reboots" flag. Both
// mean "even if someone is logged in".
type Reason struct {
	Source string    `json:"source"`
	Detail string    `json:"detail"`
	Force  bool      `json:"force"`
	Since  time.Time `json:"since"`
	// TaskID is set on a task reason: the FOG task the reboot is for.
	TaskID int `json:"task_id,omitempty"`
}

// Task is the desired-state block for a waiting FOG task.
type Task struct {
	ID    int    `json:"id"`
	Type  string `json:"type"`
	Force bool   `json:"force"`
}

// Policy is the desired-state block that applies to every reboot.
type Policy struct {
	// Grace is FOG_GRACE_TIMEOUT: seconds of warning a logged-in user
	// gets before a forced reboot. Nobody logged in means no delay.
	Grace int `json:"grace"`
}

// Decision is what the coordinator concluded for this poll.
type Decision struct {
	Reboot bool
	// Delay is the countdown to give users, zero when nobody is there.
	Delay time.Duration
	Why   string
}

// Decide applies the policy: a reason may reboot the machine now if nobody
// is logged in, or if it is forced. One qualifying reason is enough; the
// reboot satisfies all of them. Nothing here touches the OS.
func Decide(reasons []Reason, loggedIn int, p Policy) Decision {
	if len(reasons) == 0 {
		return Decision{}
	}
	details := make([]string, 0, len(reasons))
	allowed := false
	for _, r := range reasons {
		details = append(details, r.Source+": "+r.Detail)
		if loggedIn == 0 || r.Force {
			allowed = true
		}
	}
	sort.Strings(details)
	why := strings.Join(details, "; ")
	if !allowed {
		return Decision{Why: fmt.Sprintf("waiting for %d logged-in user(s) to leave (%s)", loggedIn, why)}
	}
	d := Decision{Reboot: true, Why: why}
	if loggedIn > 0 {
		d.Delay = time.Duration(p.Grace) * time.Second
		d.Why = fmt.Sprintf("%d user(s) logged in, %ds warning (%s)", loggedIn, p.Grace, why)
	}
	return d
}

// Merge records r, replacing any earlier reason from the same source: a
// source asks for one reboot, however many times it asks.
func Merge(reasons []Reason, r Reason) []Reason {
	out := Drop(reasons, r.Source)
	if r.Since.IsZero() {
		r.Since = time.Now()
	}
	return append(out, r)
}

// Drop withdraws a source's request: the task was cancelled, the
// provider no longer needs it.
func Drop(reasons []Reason, source string) []Reason {
	out := reasons[:0:0]
	for _, x := range reasons {
		if x.Source != source {
			out = append(out, x)
		}
	}
	return out
}

// loggedIn and execute are the OS-specific halves, replaced in tests.
var (
	loggedIn = osLoggedIn
	execute  = osReboot
)

// LoggedIn counts the users with a live session. Counting is deliberately
// coarse: any session, console or remote, is someone whose work a reboot
// would take away.
func LoggedIn() (int, error) {
	return loggedIn()
}

// Execute asks the OS to reboot after delay, showing message to whoever is
// logged in. It returns once the request is accepted; the reboot itself
// is asynchronous, so callers persist their state before calling this.
func Execute(delay time.Duration, message string) error {
	return execute(delay, message)
}
