package identity

import (
	"net"
	"reflect"
	"testing"
)

func iface(name, mac string, flags net.Flags) net.Interface {
	hw, _ := net.ParseMAC(mac)
	return net.Interface{Name: name, HardwareAddr: hw, Flags: flags}
}

// The golden-image case: a TAP adapter's MAC travels with the image, so only
// the hardware NIC may identify the machine.
func TestPickMACsDropsSoftwareAdapters(t *testing.T) {
	ifs := []net.Interface{
		iface("Ethernet", "52:54:00:a8:00:02", 0),
		iface("Local Area Connection", "00:ff:41:24:a3:6f", 0),
		iface("lo", "00:00:00:00:00:01", net.FlagLoopback),
		{Name: "Teredo"},
	}
	hw := func(i net.Interface) bool { return i.Name == "Ethernet" }
	if got, want := pickMACs(ifs, hw), []string{"52:54:00:a8:00:02"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pickMACs() = %v, want %v", got, want)
	}
}

// No filter, or a filter that accepts nothing, keeps the pre-filter list.
func TestPickMACsFallsBackToEveryMAC(t *testing.T) {
	ifs := []net.Interface{
		iface("tap0", "00:ff:41:24:a3:6f", 0),
		iface("eth0", "52:54:00:a8:00:02", 0),
	}
	want := []string{"00:ff:41:24:a3:6f", "52:54:00:a8:00:02"}
	for name, hw := range map[string]func(net.Interface) bool{
		"nil filter":   nil,
		"accepts none": func(net.Interface) bool { return false },
	} {
		if got := pickMACs(ifs, hw); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: pickMACs() = %v, want %v", name, got, want)
		}
	}
}
