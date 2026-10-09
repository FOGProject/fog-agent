// Package hostname is the `ensure hostname` provider (design 0001 section
// 7): the machine's name converges on the name its host record carries.
package hostname

import (
	"fmt"
	"strings"
	"time"

	"github.com/FOGProject/fog-agent/internal/provider"
)

// Desired is the hostname block of the server's desired state.
type Desired struct {
	Name string `json:"name"`
	// Enforce is the host's "Enforce Hostname | AD Join Reboots" flag: the
	// admin's permission to reboot to finish a rename. Nothing here
	// reboots; a rename that needs one is reported as pending_reboot and
	// the reboot coordinator, when it exists, will consult this.
	Enforce bool `json:"enforce"`
	// WaitUntil is set while the server holds a rename of a domain member
	// in its join cooldown (design 0017): the UTC time, RFC 3339, after
	// which the rename may go ahead. Empty otherwise.
	WaitUntil string `json:"wait_until"`
}

// current and set are the OS-specific halves, replaced in tests. joined
// and pending matter on Windows only, where a domain member must not be
// renamed alone (design 0017): joined says the machine is in an AD
// domain, and pending is the name it will have after its next boot, or
// empty.
var (
	current = osCurrent
	set     = osSet
	joined  = osJoined
	pending = osPending
)

// Current is the name the machine runs under now.
func Current() (string, error) { return current() }

// Pending is the name the machine will have after its next boot, or empty
// where the platform has no such thing or nothing is pending.
func Pending() string { return pending() }

// Ensure reconciles the machine's name with d and reports. Names compare
// case-insensitively: DNS and NetBIOS both do, and a rename that only
// changes case is not a rename anyone asked for.
func Ensure(d Desired) provider.Result {
	want := strings.TrimSpace(d.Name)
	if want == "" {
		return provider.Result{Status: provider.StatusFailed, Detail: "desired name is empty"}
	}
	have, err := current()
	if err != nil {
		return provider.Result{Status: provider.StatusFailed, Detail: "reading the hostname: " + err.Error()}
	}
	if strings.EqualFold(have, want) {
		return provider.Result{Status: provider.StatusUnchanged, Detail: have}
	}
	if joined() {
		// A domain member renamed alone comes back under a name its
		// computer object does not carry, and its secure channel fails.
		// The directory capability renames both together, with the
		// credential only it is sent, and leaves the new name pending.
		detail := fmt.Sprintf("%s -> %s", have, want)
		if p := pending(); p != "" && strings.EqualFold(p, want) {
			return provider.Result{Status: provider.StatusPendingReboot,
				Detail: detail + ", renamed in the domain"}
		}
		if when := waitText(d.WaitUntil); when != "" {
			return provider.Result{Status: provider.StatusPending,
				Detail: detail + ": the server holds domain renames for an hour after a join or rename; " +
					"this one runs after " + when}
		}
		return provider.Result{Status: provider.StatusFailed,
			Detail: detail + ": this machine is in a domain, so its computer object must be renamed too, " +
				"and the server sent no rename. That needs FOG 1.6.0-RC-8 or later and the host's AD domain, " +
				"username and password"}
	}
	reboot, err := set(want)
	if err != nil {
		return provider.Result{Status: provider.StatusFailed, Detail: fmt.Sprintf("%s -> %s: %v", have, want, err)}
	}
	detail := fmt.Sprintf("%s -> %s", have, want)
	if reboot {
		return provider.Result{Status: provider.StatusPendingReboot, Detail: detail}
	}
	return provider.Result{Status: provider.StatusApplied, Detail: detail}
}

// waitText renders the server's wait time in this machine's local time, so
// it reads like the log lines around it. An unparsable value is shown as
// sent rather than dropped: it still says the rename waits.
func waitText(utc string) string {
	utc = strings.TrimSpace(utc)
	if utc == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, utc)
	if err != nil {
		return utc
	}
	return t.Local().Format("2006-01-02 15:04 MST")
}
