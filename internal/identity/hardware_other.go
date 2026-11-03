//go:build !linux && !windows

package identity

import "net"

// hardwareFilter has no hardware test on this OS, so the agent sends every
// MAC, as it did before the filter.
func hardwareFilter() func(net.Interface) bool { return nil }
