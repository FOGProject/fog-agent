//go:build !linux && !windows

package directory

// gather has no implementation here. False means "no collector ran", which
// keeps the block off the poll entirely -- a zero Directory would tell the
// server the machine had left its domain (design 0009 §3).
func gather() (Directory, bool) {
	return Directory{}, false
}
