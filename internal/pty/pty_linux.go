//go:build linux

package pty

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// openMaster is posix_openpt + unlockpt + ptsname through the Linux ioctls
// (grantpt is a no-op with devpts).
func openMaster() (*os.File, string, error) {
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("pty: open /dev/ptmx: %w", err)
	}
	var n uint32
	err = control(f, func(fd uintptr) error {
		var unlock int32
		if err := ioctl(fd, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
			return fmt.Errorf("pty: unlockpt: %w", err)
		}
		if err := ioctl(fd, syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
			return fmt.Errorf("pty: ptsname: %w", err)
		}
		return nil
	})
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, fmt.Sprintf("/dev/pts/%d", n), nil
}

// configureSlave turns on IUTF8 so canonical-mode erase removes a whole UTF-8
// character rather than one byte of it, as tmux does for UTF-8 terminals.
func configureSlave(f *os.File) {
	var t syscall.Termios
	fd := f.Fd()
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t))); errno != 0 {
		return
	}
	t.Iflag |= syscall.IUTF8
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t)))
}
