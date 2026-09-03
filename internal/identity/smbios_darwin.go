package identity

import (
	"os/exec"
	"regexp"
)

var (
	reUUID   = regexp.MustCompile(`"IOPlatformUUID" = "([^"]+)"`)
	reSerial = regexp.MustCompile(`"IOPlatformSerialNumber" = "([^"]+)"`)
)

// readSMBIOS: Apple exposes the platform UUID and serial through IOKit and
// nothing for board serial or asset tag, so those stay empty (design doc
// 4.1). ioreg is the tool every Mac agent ends up shelling to.
func readSMBIOS() Host {
	var h Host
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		h.Warnings = append(h.Warnings, "ioreg: "+err.Error())
		return h
	}
	if m := reUUID.FindSubmatch(out); m != nil {
		h.SystemUUID = string(m[1])
	}
	if m := reSerial.FindSubmatch(out); m != nil {
		h.SystemSerial = string(m[1])
	}
	return h
}
