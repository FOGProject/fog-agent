package identity

import (
	"net"
	"os"
	"path/filepath"
)

// sysClassNet is a variable so a test can point it at a fake sysfs tree.
var sysClassNet = "/sys/class/net"

// hardwareFilter accepts an interface that the kernel backs with a device:
// /sys/class/net/<name>/device exists for a PCI, USB or virtio NIC, and not
// for a bridge, a bond, a tun/tap or a veth. It returns nil when sysfs is not
// readable, so the caller sends every MAC.
func hardwareFilter() func(net.Interface) bool {
	if _, err := os.Stat(sysClassNet); err != nil {
		return nil
	}
	return func(i net.Interface) bool {
		_, err := os.Stat(filepath.Join(sysClassNet, i.Name, "device"))
		return err == nil
	}
}
