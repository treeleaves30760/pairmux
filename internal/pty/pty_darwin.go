//go:build darwin

package pty

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// openMaster is posix_openpt + grantpt + unlockpt + ptsname, through the
// ioctls libc itself uses on darwin.
func openMaster() (*os.File, string, error) {
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("pty: open /dev/ptmx: %w", err)
	}
	var name [128]byte
	err = control(f, func(fd uintptr) error {
		if err := ioctl(fd, syscall.TIOCPTYGRANT, nil); err != nil {
			return fmt.Errorf("pty: grantpt: %w", err)
		}
		if err := ioctl(fd, syscall.TIOCPTYUNLK, nil); err != nil {
			return fmt.Errorf("pty: unlockpt: %w", err)
		}
		if err := ioctl(fd, syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil {
			return fmt.Errorf("pty: ptsname: %w", err)
		}
		return nil
	})
	if err != nil {
		f.Close()
		return nil, "", err
	}
	n := bytes.IndexByte(name[:], 0)
	if n < 0 {
		n = len(name)
	}
	return f, string(name[:n]), nil
}

// configureSlave leaves darwin's default line discipline alone: it already
// erases with DEL and handles UTF-8 in canonical mode.
func configureSlave(*os.File) {}
