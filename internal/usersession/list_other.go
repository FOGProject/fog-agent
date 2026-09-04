//go:build !linux && !windows

package usersession

// list has no collector on this platform. False, not an empty slice: an empty
// open set is a claim that nobody is logged on, and the server acts on it by
// closing every session it holds for the host.
func list() ([]Session, bool) { return nil, false }
