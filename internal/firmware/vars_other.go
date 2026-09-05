//go:build !linux && !windows

package firmware

// macOS and anything else: there is no UEFI boot manager to ask, and Apple
// platforms do not netboot into FOS. Unsupported is the honest answer, and
// the caller turns it into "this machine cannot be netbooted" rather than
// into a reboot that would go nowhere.

func osReadVar(string) ([]byte, error) { return nil, ErrUnsupported }

func osWriteVar(string, []byte) error { return ErrUnsupported }

func osDeleteVar(string) error { return ErrUnsupported }
