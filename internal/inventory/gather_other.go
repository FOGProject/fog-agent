//go:build !linux && !windows

package inventory

// gather has no collector on this platform yet. The false says so, and the
// caller sends no inventory block at all -- reporting an empty snapshot
// would blank a good row on the server. The macOS
// (system_profiler) collector is its own commit; see 0006 section 6.
func gather() (Inventory, bool) { return Inventory{}, false }
