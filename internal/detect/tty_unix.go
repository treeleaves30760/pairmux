//go:build darwin || linux

package detect

import (
	"os"
	"syscall"
	"unsafe"
)

// readTTYState fetches termios through the same ioctl stty uses, in-process:
// the alternative is spawning stty per poll, which at several agents each
// polling one terminal is thousands of times the cost of an ioctl.
func readTTYState(path string) TTYState {
	// O_NOCTTY so a pairmux command can never adopt the pane's terminal as its
	// own, and O_NONBLOCK so opening cannot block on a device with no writer.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return TTYState{}
	}
	defer f.Close()

	var t syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		uintptr(ioctlReadTermios), uintptr(unsafe.Pointer(&t))); errno != 0 {
		return TTYState{}
	}
	return TTYState{
		Echo:      t.Lflag&syscall.ECHO != 0,
		Canonical: t.Lflag&syscall.ICANON != 0,
		Known:     true,
	}
}
