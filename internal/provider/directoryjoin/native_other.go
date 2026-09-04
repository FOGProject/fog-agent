//go:build !linux && !windows

package directoryjoin

import "context"

// Native is the join tooling of this platform: none. macOS binds to a
// directory through dsconfigad, which is a different operation against a
// different service, and design 0009 does not cover it.
func Native() Backend { return unsupported{} }

type unsupported struct{}

func (unsupported) Available() (bool, string) {
	return false, "joining a directory is not implemented on this platform"
}

func (unsupported) Join(context.Context, Policy) Result {
	return Result{Status: StatusUnsupported,
		Error: "joining a directory is not implemented on this platform"}
}
