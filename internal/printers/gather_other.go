//go:build !linux && !windows

package printers

// gather has no implementation on this platform, so it reports that no
// collector ran rather than an empty printer list. See Gather's comment: an
// empty list is a claim about the machine, and this is not one.
func gather() (Printers, bool) { return Printers{}, false }
