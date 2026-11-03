package identity

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// A NIC has a device link under /sys/class/net/<name>; a tap does not.
func TestHardwareFilterUsesSysfsDevice(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"eth0/device", "tap0"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := sysClassNet
	sysClassNet = root
	t.Cleanup(func() { sysClassNet = old })

	hw := hardwareFilter()
	if hw == nil {
		t.Fatal("hardwareFilter() = nil with a readable sysfs")
	}
	if !hw(net.Interface{Name: "eth0"}) || hw(net.Interface{Name: "tap0"}) {
		t.Fatal("hardwareFilter() must accept eth0 and reject tap0")
	}

	sysClassNet = filepath.Join(root, "missing")
	if hardwareFilter() != nil {
		t.Fatal("hardwareFilter() must be nil without sysfs")
	}
}
