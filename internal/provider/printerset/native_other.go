//go:build !windows

package printerset

// Native is the print subsystem of this platform. CUPS everywhere but
// Windows: it is what Linux and macOS both print through, and the same
// lpadmin drives both.
func Native() Backend { return CUPS{} }
