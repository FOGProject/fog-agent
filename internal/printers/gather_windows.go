//go:build windows

package printers

import (
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// EnumPrinters flags. LOCAL is the queues defined on this machine;
// CONNECTIONS is the per-user connections to queues shared from elsewhere.
// Both are printers the user can print to and both are printers FOG may have
// put there, so both are reported.
const (
	printerEnumLocal       = 0x00000002
	printerEnumConnections = 0x00000004
)

// PRINTER_ATTRIBUTE_SHARED.
const printerAttributeShared = 0x00000008

// Standard TCP/IP Port Monitor protocol values, as it writes them.
const (
	portProtocolRaw = 1
	portProtocolLPR = 2
)

// portsKey is where the Standard TCP/IP Port Monitor records what each port
// actually points at. The spooler only ever gives us the port's *name*, which
// an admin chose and which means nothing on any other machine; the address is
// here.
const portsKey = `SYSTEM\CurrentControlSet\Control\Print\Monitors\Standard TCP/IP Port\Ports`

var (
	winspool             = syscall.NewLazyDLL("winspool.drv")
	procEnumPrintersW    = winspool.NewProc("EnumPrintersW")
	procGetDefaultPrintr = winspool.NewProc("GetDefaultPrinterW")
)

// printerInfo2 is PRINTER_INFO_2W. The string fields point into the same
// buffer EnumPrinters filled, so every one of them is converted before that
// buffer goes out of scope.
type printerInfo2 struct {
	ServerName         *uint16
	PrinterName        *uint16
	ShareName          *uint16
	PortName           *uint16
	DriverName         *uint16
	Comment            *uint16
	Location           *uint16
	DevMode            uintptr
	SepFile            *uint16
	PrintProcessor     *uint16
	Datatype           *uint16
	Parameters         *uint16
	SecurityDescriptor uintptr
	Attributes         uint32
	Priority           uint32
	DefaultPriority    uint32
	StartTime          uint32
	UntilTime          uint32
	Status             uint32
	Jobs               uint32
	AveragePPM         uint32
}

// gather asks the spooler directly rather than shelling out to PowerShell.
//
// Same reason the directory collector calls NetGetJoinInformation: a
// PowerShell launch costs a second or more on a cold machine and the legacy
// client's habit of shelling out for everything (design 0010 §1.2's `echo |
// tr` to slugify a string) is exactly what this rebuild is getting away from.
func gather() (Printers, bool) {
	infos, ok := enumPrinters()
	if !ok {
		// The spooler declined to answer -- service stopped, or a call that
		// failed for a reason we cannot see. Report nothing rather than an
		// empty list, which would tell the server this machine's printers
		// had all been removed.
		return Printers{}, false
	}

	p := Printers{
		Subsystem: SubsystemWinspool,
		Default:   defaultPrinter(),
		Installed: make([]Printer, 0, len(infos)),
	}
	for i := range infos {
		port := utf16PtrToString(infos[i].PortName)
		p.Installed = append(p.Installed, Printer{
			Name:   utf16PtrToString(infos[i].PrinterName),
			URI:    uriForPort(port),
			Driver: utf16PtrToString(infos[i].DriverName),
			Shared: infos[i].Attributes&printerAttributeShared != 0,
		})
	}
	return p, true
}

// enumPrinters does the two-call sizing dance EnumPrinters requires: the
// first call fails with the byte count it wants, the second fills it.
//
// The buffer is allocated as a []printerInfo2 rather than a []byte so it is
// correctly aligned for the struct being read out of it -- a []byte carries
// no such guarantee.
func enumPrinters() ([]printerInfo2, bool) {
	flags := uintptr(printerEnumLocal | printerEnumConnections)
	var needed, returned uint32

	// The sizing call. It FAILS with ERROR_INSUFFICIENT_BUFFER when there is
	// something to return, and SUCCEEDS with needed=0 when there is not --
	// so the return value is what separates "no printers" from "the call
	// broke", and needed alone cannot.
	sized, _, _ := procEnumPrintersW.Call(
		flags, 0, 2, 0, 0,
		uintptr(unsafe.Pointer(&needed)),
		uintptr(unsafe.Pointer(&returned)),
	)
	if needed == 0 {
		return nil, sized != 0
	}

	size := uint32(unsafe.Sizeof(printerInfo2{}))
	count := (needed + size - 1) / size
	buf := make([]printerInfo2, count)
	r, _, _ := procEnumPrintersW.Call(
		flags, 0, 2,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(count*size),
		uintptr(unsafe.Pointer(&needed)),
		uintptr(unsafe.Pointer(&returned)),
	)
	if r == 0 {
		return nil, false
	}
	if returned > count {
		returned = count
	}
	// The strings hang off this same backing array, so the caller reads them
	// while the slice it was handed still holds it alive.
	return buf[:returned], true
}

// defaultPrinter wraps GetDefaultPrinter. A machine with no default fails the
// call, which is a real answer and not an error worth reporting.
func defaultPrinter() string {
	var size uint32
	procGetDefaultPrintr.Call(0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return ""
	}
	buf := make([]uint16, size)
	r, _, _ := procGetDefaultPrintr.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// uriForPort turns a Windows port name into the device URI design 0010 §2
// describes a printer with, so a Windows row and a CUPS row for the same
// physical device say the same thing.
//
// This is the reconstruction that makes one printer entry serve both
// platforms. `socket://10.0.4.20:9100` on CUPS and a Standard TCP/IP port
// pointed at 10.0.4.20:9100 on Windows are the same printer, and until they
// are written the same way nothing can tell that they are.
func uriForPort(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		return ""
	}
	// A port monitor that already names a URI (WSD, IPP, some vendor
	// monitors) has done the work; do not second-guess it.
	if strings.Contains(port, "://") {
		return normalizeURI(port)
	}
	// A connection to a shared queue: \\server\printer.
	if rest, found := strings.CutPrefix(port, `\\`); found {
		host, share, ok := strings.Cut(rest, `\`)
		if ok && host != "" && share != "" {
			return "smb://" + host + "/" + share
		}
		return opaqueURI(port)
	}
	if uri := tcpPortURI(port); uri != "" {
		return uri
	}
	// USB00x, FILE:, PORTPROMPT:, nul: and anything a third-party monitor
	// invented. Real queues with no expressible address -- reported as such
	// rather than blanked or guessed at.
	return opaqueURI(port)
}

// tcpPortURI reads the Standard TCP/IP Port Monitor's own registry record for
// one port. Empty when the port is not one of its, or when the record holds
// no address.
func tcpPortURI(port string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, portsKey+`\`+port, registry.READ)
	if err != nil {
		return ""
	}
	defer k.Close()

	host, _, err := k.GetStringValue("HostName")
	if err != nil || strings.TrimSpace(host) == "" {
		// HostName is the DNS name and is empty on a port configured by
		// address. IPAddress is the fallback, and the monitor always writes
		// one of the two.
		host, _, err = k.GetStringValue("IPAddress")
		if err != nil {
			return ""
		}
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}

	protocol, _, err := k.GetIntegerValue("Protocol")
	if err != nil {
		protocol = portProtocolRaw
	}
	switch protocol {
	case portProtocolLPR:
		queue, _, err := k.GetStringValue("Queue")
		if err != nil {
			queue = ""
		}
		return "lpd://" + host + "/" + strings.TrimSpace(queue)
	case portProtocolRaw:
		fallthrough
	default:
		portNum, _, err := k.GetIntegerValue("PortNumber")
		if err != nil || portNum == 0 {
			// 9100 is the RAW default and what the monitor uses when the
			// value is absent.
			portNum = 9100
		}
		return "socket://" + host + ":" + strconv.FormatUint(portNum, 10)
	}
}

// utf16PtrToString reads a NUL-terminated UTF-16 string the spooler wrote
// into a buffer we own.
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	var out []uint16
	for i := 0; ; i++ {
		c := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(i)*2))
		if c == 0 {
			break
		}
		out = append(out, c)
	}
	return syscall.UTF16ToString(out)
}
