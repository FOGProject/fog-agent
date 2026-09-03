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
	StatusFailed        = "failed"
)

// Result is what a provider reports after one reconcile.
type Result struct {
	Status string
	Detail string
}
