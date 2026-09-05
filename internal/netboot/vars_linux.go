//go:build linux

package netboot

import (
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// efivarsDir is where the kernel exposes UEFI variables. Its absence is the
// signal that this machine has no UEFI to talk to.
var efivarsDir = "/sys/firmware/efi/efivars"

func varPath(name string) string {
	return efivarsDir + "/" + name + "-" + EFIGlobal
}

// efivarfs frames every variable as a 4-byte attribute word followed by the
// data, on read AND on write. The Win32 calls have no attribute word at
// all, which is why these two platforms cannot share a single read helper.
const attrWordLen = 4

// fsImmutableFl is FS_IMMUTABLE_FL from <linux/fs.h>. x/sys/unix exports
// the two flag ioctls but not the flag itself, and its value is part of the
// kernel's stable userspace ABI.
const fsImmutableFl = 0x00000010

// NV | BS | RT: non-volatile, available to boot services, available at
// runtime. BootNext must survive the reboot that consumes it, so NV is not
// optional, and these are the attributes the firmware's own BootNext
// carries.
const bootNextAttrs = 0x01 | 0x02 | 0x04

func osReadVar(name string) ([]byte, error) {
	b, err := os.ReadFile(varPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			// A missing variable and a machine with no UEFI both surface
			// as ENOENT on the same path, and they mean opposite things
			// to an admin. The directory tells them apart.
			if _, statErr := os.Stat(efivarsDir); statErr != nil {
				return nil, ErrUnsupported
			}
			return nil, fmt.Errorf("netboot: firmware variable %s is not set", name)
		}
		return nil, err
	}
	if len(b) < attrWordLen {
		return nil, fmt.Errorf("netboot: %s is %d bytes, shorter than the attribute word", name, len(b))
	}
	return b[attrWordLen:], nil
}

// osWriteVar writes attributes+data in a single write(2). efivarfs requires
// the whole variable in one call -- a partial write is rejected rather than
// buffered -- so this deliberately does not use a buffered writer.
func osWriteVar(name string, data []byte) error {
	path := varPath(name)
	buf := make([]byte, attrWordLen+len(data))
	binary.LittleEndian.PutUint32(buf[:attrWordLen], bootNextAttrs)
	copy(buf[attrWordLen:], data)

	// An existing efivarfs file carries the immutable flag: the kernel's
	// guard against a careless recursive delete bricking a machine by
	// removing its firmware variables. Writing fails with EPERM until it
	// is cleared, even as root. Clear it for this write and put it back.
	restore, err := clearImmutable(path)
	if err != nil {
		return err
	}
	defer restore()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		if os.IsNotExist(err) {
			if _, statErr := os.Stat(efivarsDir); statErr != nil {
				return ErrUnsupported
			}
		}
		return err
	}
	defer f.Close()
	n, err := f.Write(buf)
	if err != nil {
		return err
	}
	if n != len(buf) {
		return fmt.Errorf("netboot: short write to %s: %d of %d bytes", name, n, len(buf))
	}
	return nil
}

func osDeleteVar(name string) error {
	path := varPath(name)
	restore, err := clearImmutable(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Not deferred: the file is about to stop existing, and restoring a
	// flag on a deleted path would just fail.
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		restore()
	}
	return err
}

// clearImmutable drops FS_IMMUTABLE_FL from path if it is set, returning a
// function that puts it back. A path that does not exist yet has no flags
// to clear, which is the create case and not an error.
func clearImmutable(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return func() {}, nil
		}
		return nil, err
	}
	fd := int(f.Fd())
	flags, err := unix.IoctlGetInt(fd, unix.FS_IOC_GETFLAGS)
	if err != nil {
		// Not every filesystem implements the flag ioctls. If this one
		// does not, there is no immutable bit to be blocked by either.
		f.Close()
		return func() {}, nil
	}
	if flags&fsImmutableFl == 0 {
		f.Close()
		return func() {}, nil
	}
	if err := unix.IoctlSetPointerInt(fd, unix.FS_IOC_SETFLAGS, flags&^fsImmutableFl); err != nil {
		f.Close()
		return nil, fmt.Errorf("netboot: could not clear the immutable flag on %s: %w", path, err)
	}
	return func() {
		_ = unix.IoctlSetPointerInt(fd, unix.FS_IOC_SETFLAGS, flags)
		f.Close()
	}, nil
}
