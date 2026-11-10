//go:build !windows

package main

// readImageState has nothing to read off Windows: there is no Sysprep setup
// to wait for, so it reports the value as absent, which means finished.
func readImageState() (string, bool, error) {
	return "", false, nil
}
