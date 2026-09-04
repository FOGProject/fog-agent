//go:build !linux

package inventory

// gather has no collector on this platform yet. The false says so, and the
// caller sends no inventory block at all -- reporting an empty snapshot
// would blank a good row on the server. Windows (CIM) and macOS
// (system_profiler) collectors are their own commits; see 0006 section 6.
func gather() (Inventory, bool) { return Inventory{}, false }
