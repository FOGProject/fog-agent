//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// readImageState reads HKLM\SYSTEM\Setup\State\ImageState, the value Windows
// setup updates as it moves through its passes after Sysprep.
func readImageState() (string, bool, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\Setup\State`, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue("ImageState")
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}
