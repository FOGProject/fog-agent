//go:build !windows

package hostname

// Only Windows renames a domain member through the directory (design 0017
// section 3.5). Everywhere else the rename stays local.
func osJoined() bool    { return false }
func osPending() string { return "" }
