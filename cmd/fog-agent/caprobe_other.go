//go:build !windows

package main

import (
	"errors"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// writeInstallerKeys is the MSI wizard's way of reading a value back from a
// program it ran, so it exists only where there is an MSI.
func writeInstallerKeys(*enroll.CAProbe) error {
	return errors.New("--registry is for the Windows installer and does nothing here")
}

func clearInstallerKeys() error { return nil }
