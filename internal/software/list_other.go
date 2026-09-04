//go:build !linux && !windows

package software

// list has no collector on this platform yet. It returns nil rather than an
// empty slice, and the poll loop treats a nil list as "nothing to report"
// rather than "everything was uninstalled" -- the distinction matters,
// because the server's reconcile marks anything missing from a reported list
// as removed. macOS collectors are their own commit; see 0006 section 6.
func list() ([]Program, bool) { return nil, false }
