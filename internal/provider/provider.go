// Package provider holds what every capability provider has in common:
// the result it reports. One implementation per OS is chosen at build
// time by build tags, so "not supported here" is a compile-time fact
// (design 0001 section 6).
package provider

// Result statuses, as the server's State::RESULT_STATUSES spells them.
const (
	StatusApplied       = "applied"
	StatusUnchanged     = "unchanged"
	StatusPendingReboot = "pending_reboot"
	// StatusPending is work that waits on something outside the agent,
	// such as a rename the server holds in its join cooldown. Nothing
	// failed. Sent only when the server said why it waits, so a server
	// that does not know the status never receives it.
	StatusPending = "pending"
	StatusFailed  = "failed"
)

// Result is what a provider reports after one reconcile.
type Result struct {
	Status string
	Detail string
}
