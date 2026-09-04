//go:build windows

package inventory

import (
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"

	"github.com/FOGProject/fog-agent/internal/identity"
	"github.com/FOGProject/fog-agent/internal/identity/smbios"
)

// gather reads hardware from the firmware table the agent already parses
// for its identity, plus two Win32 calls for the things SMBIOS does not
// describe: the boot disk and the display adapters.
//
// No WMI and no PowerShell. Both would work, and both would mean spawning
// a process (or initializing COM) on every fact collection to read values
// this process can ask the kernel for directly -- and WMI in particular is
// the part of Windows most likely to be broken on the machine an admin is
// trying to inventory.
func gather() (Inventory, bool) {
	var inv Inventory

	table, major, minor, err := identity.RawSMBIOS()
	if err == nil {
		if hw, perr := smbios.ParseHardware(table, major, minor); perr == nil {
			inv.SysMan, inv.SysProduct = hw.SysMan, hw.SysProduct
			inv.SysVersion, inv.SysSerial = hw.SysVersion, hw.SysSerial
			inv.SysType = chassisTypeName(hw.CaseType)
			inv.BIOSVendor, inv.BIOSVersion, inv.BIOSDate = hw.BIOSVendor, hw.BIOSVersion, hw.BIOSDate
			inv.MBMan, inv.MBProductName = hw.MBMan, hw.MBProductName
			inv.MBVersion, inv.MBSerial, inv.MBAsset = hw.MBVersion, hw.MBSerial, hw.MBAsset
			inv.CaseMan, inv.CaseVer = hw.CaseMan, hw.CaseVer
			inv.CaseSerial, inv.CaseAsset = hw.CaseSerial, hw.CaseAsset
			inv.CPUMan, inv.CPUVersion = hw.CPUMan, hw.CPUVersion
			inv.CPUCurrent, inv.CPUMax = hw.CPUCurrent, hw.CPUMax
			inv.Mem = hw.Mem
		}
	}
	// SMBIOS types 4 and 17 are optional, and a hypervisor is entitled to
	// leave them out. VirtualBox does: the lab VM's table carries types
	// 0, 1, 2, 3, 11 and nothing else, so everything above resolves and the
	// processor and memory come back empty. Real firmware does emit them
	// (verified against dmidecode on a Precision 7550: 3900/5100 MHz,
	// 32768 MB), which is exactly why this had to be found on a VM.
	//
	// The fallbacks are the same kind of call as the rest of this file --
	// a registry read and a kernel call, no WMI -- and they run only when
	// SMBIOS said nothing, because when it speaks it is the better source:
	// type 4 reports the real current and max clocks, where the registry
	// only carries the nominal one, and type 17 reports installed modules
	// rather than what the OS can address.
	if inv.CPUVersion == "" {
		inv.CPUMan, inv.CPUVersion, inv.CPUCurrent = cpuFromRegistry()
	}
	if inv.Mem == "" {
		inv.Mem = physicalMemoryMB()
	}

	// The system UUID comes from the identity view rather than being parsed
	// twice: it is the value enrollment bound this agent's certificate to,
	// and the inventory row must agree with it.
	inv.SysUUID = identity.Read().Identity.SystemUUID

	inv.HDModel, inv.HDSerial, inv.HDFirmware = primaryDisk()
	inv.GPUVendors, inv.GPUProducts = gpus()

	return inv, inv != Inventory{}
}

// ---------------------------------------------------------------- the disk

var (
	procCreateFileW          = kernel32.NewProc("CreateFileW")
	procDeviceIoCtl          = kernel32.NewProc("DeviceIoControl")
	procCloseHandle          = kernel32.NewProc("CloseHandle")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	user32                   = syscall.NewLazyDLL("user32.dll")
	procEnumDisplayD         = user32.NewProc("EnumDisplayDevicesW")
)

// IOCTL_STORAGE_QUERY_PROPERTY, and the StorageDeviceProperty query that
// returns the vendor, product, revision and serial the drive reports.
const (
	ioctlStorageQueryProperty = 0x2D1400
	storageDeviceProperty     = 0
	propertyStandardQuery     = 0
)

// storagePropertyQuery is the input buffer for IOCTL_STORAGE_QUERY_PROPERTY.
type storagePropertyQuery struct {
	PropertyId uint32
	QueryType  uint32
	// AdditionalParameters[1] in the Win32 header. Go pads the struct to
	// 12 bytes on its own, which is what the C compiler does too; the size
	// assertion below is what actually holds that.
	AdditionalParameters [1]byte
}

// storageDeviceDescriptor is the output header. The strings it names live
// further into the same buffer, at byte offsets from its start, which is
// why the caller keeps the whole buffer rather than just this struct.
type storageDeviceDescriptor struct {
	Version               uint32
	Size                  uint32
	DeviceType            byte
	DeviceTypeModifier    byte
	RemovableMedia        byte
	CommandQueueing       byte
	VendorIdOffset        uint32
	ProductIdOffset       uint32
	ProductRevisionOffset uint32
	SerialNumberOffset    uint32
	// The bus type and ID fields follow; unread.
}

// The two structs above are ABI, not Go's to lay out: a wrong size means
// DeviceIoControl reads past the input buffer or the offsets land on the
// wrong words, and the symptom is a plausible-looking wrong serial rather
// than a crash. These make that a compile error instead. An index into a
// one-element array is only legal at 0, so a size that is not the expected
// one fails to build -- on every GOOS=windows build, including CI's.
//
// STORAGE_PROPERTY_QUERY is 4 + 4 + 1, padded to 12. The descriptor is read
// as a prefix: 8 bytes of version and size, four single-byte fields, then
// the four DWORD offsets this code actually uses, which is 28. The real
// struct continues past that and is deliberately not declared, because
// nothing here reads the bus type or the raw properties.
var (
	_ = [1]struct{}{}[unsafe.Sizeof(storagePropertyQuery{})-12]
	_ = [1]struct{}{}[unsafe.Sizeof(storageDeviceDescriptor{})-28]
	_ = [1]struct{}{}[unsafe.Sizeof(displayDevice{})-840]
)

// primaryDisk reports the boot disk's model, serial and firmware.
//
// PhysicalDrive0 rather than a search: the inventory table holds exactly
// one disk, and drive 0 is the one the machine booted from. A machine with
// several disks needs a row per device, which is a separate change (0007).
func primaryDisk() (model, serial, firmware string) {
	h, err := openDevice(`\\.\PhysicalDrive0`)
	if err != nil {
		return "", "", ""
	}
	defer procCloseHandle.Call(h)

	query := storagePropertyQuery{PropertyId: storageDeviceProperty, QueryType: propertyStandardQuery}
	// 1 KB: the descriptor header is 40 bytes and the strings that follow
	// it are short, so this cannot truncate in practice, and an oversized
	// stack buffer costs nothing.
	buf := make([]byte, 1024)
	var returned uint32
	ok, _, _ := procDeviceIoCtl.Call(
		h,
		uintptr(ioctlStorageQueryProperty),
		uintptr(unsafe.Pointer(&query)),
		unsafe.Sizeof(query),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&returned)),
		0,
	)
	if ok == 0 || returned < uint32(unsafe.Sizeof(storageDeviceDescriptor{})) {
		return "", "", ""
	}
	d := (*storageDeviceDescriptor)(unsafe.Pointer(&buf[0]))
	view := buf[:returned]
	// The vendor is usually blank on NVMe, where the whole name is in the
	// product id, so they are joined rather than reported separately: the
	// inventory row has one model field.
	model = strings.TrimSpace(strings.TrimSpace(descriptorString(view, d.VendorIdOffset)) +
		" " + strings.TrimSpace(descriptorString(view, d.ProductIdOffset)))
	serial = strings.TrimSpace(descriptorString(view, d.SerialNumberOffset))
	firmware = strings.TrimSpace(descriptorString(view, d.ProductRevisionOffset))
	return model, serial, firmware
}

// descriptorString reads a NUL-terminated ASCII string at a byte offset
// into the descriptor buffer. Offset 0 means the device did not report
// that field -- it is not the start of the buffer.
func descriptorString(buf []byte, off uint32) string {
	if off == 0 || int(off) >= len(buf) {
		return ""
	}
	s := buf[off:]
	if i := indexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return string(s)
}

// indexByte is bytes.IndexByte, spelled out to keep this file's imports to
// the syscall surface it is about.
func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// openDevice opens a device path for the metadata IOCTL: no access rights
// requested, which is what lets an unprivileged process query a drive it
// may not read.
func openDevice(path string) (uintptr, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	const (
		fileShareReadWrite = 0x00000001 | 0x00000002
		openExisting       = 3
	)
	h, _, callErr := procCreateFileW.Call(
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(fileShareReadWrite),
		0,
		uintptr(openExisting),
		0,
		0,
	)
	if h == uintptr(syscall.InvalidHandle) {
		return 0, callErr
	}
	return h, nil
}

// ---------------------------------------------------------------- the GPUs

// displayDevice is DISPLAY_DEVICEW. The two 128-rune arrays are the reason
// this is declared rather than read field by field: their sizes are part of
// the ABI and cb must be the whole struct.
type displayDevice struct {
	cb           uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

// gpus reports display adapters as the two comma-joined strings the
// inventory table keeps.
//
// EnumDisplayDevices gives one adapter name per index; it does not split
// vendor from product the way lspci does, so the whole name goes in
// products and vendors carries the leading word -- "NVIDIA" out of "NVIDIA
// Quadro T2000". Guessing further would be inventing structure the API
// does not provide.
func gpus() (vendors, products string) {
	var v, p []string
	seen := map[string]bool{}
	for i := 0; i < 16; i++ {
		var d displayDevice
		d.cb = uint32(unsafe.Sizeof(d))
		ok, _, _ := procEnumDisplayD.Call(0, uintptr(i), uintptr(unsafe.Pointer(&d)), 0)
		if ok == 0 {
			break
		}
		// One adapter reports once per attached monitor, so the same name
		// arrives several times on a docked laptop.
		name := strings.TrimSpace(syscall.UTF16ToString(d.DeviceString[:]))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		p = append(p, name)
		v = append(v, strings.Fields(name)[0])
	}
	return strings.Join(v, ","), strings.Join(p, ",")
}

// ------------------------------------------- processor and memory fallbacks

// cpuFromRegistry reads the processor description the kernel publishes for
// CPU 0. Windows writes this key from CPUID at boot on every machine,
// physical or virtual, so it is there when SMBIOS type 4 is not.
//
// ~MHz is the nominal clock, not the current one -- which is why this is a
// fallback and not the primary source. There is no max-speed equivalent
// here, so CPUMax is deliberately left as SMBIOS found it (empty): a
// guessed maximum is worse than a blank field in an inventory.
func cpuFromRegistry() (man, version, mhz string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return "", "", ""
	}
	defer k.Close()
	man, _, _ = k.GetStringValue("VendorIdentifier")
	version, _, _ = k.GetStringValue("ProcessorNameString")
	if hz, _, err := k.GetIntegerValue("~MHz"); err == nil && hz > 0 {
		mhz = strconv.FormatUint(hz, 10)
	}
	return strings.TrimSpace(man), strings.TrimSpace(version), mhz
}

// memoryStatusEx is Win32 MEMORYSTATUSEX. dwLength must be set to the size
// of the struct before the call or GlobalMemoryStatusEx fails.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// A wrong layout here reads garbage rather than failing, so pin the size the
// same way the disk and display structs in this file are pinned.
var _ = [1]struct{}{}[unsafe.Sizeof(memoryStatusEx{})-64]

// physicalMemoryMB reports RAM the OS can address, in MB, matching what the
// Linux gatherer takes from /proc/meminfo. It is a few MB below the installed
// total because firmware reserves some; SMBIOS type 17, when present, is
// preferred precisely because it reports what is fitted.
func physicalMemoryMB() string {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 || m.TotalPhys == 0 {
		return ""
	}
	return strconv.FormatUint(m.TotalPhys/(1024*1024), 10)
}
