//go:build darwin || linux

package muxd

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

type unixTerminal struct {
	fd    uintptr
	saved syscall.Termios
}

func termios(fd uintptr, req uintptr, t *syscall.Termios) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t))); errno != 0 {
		return errno
	}
	return nil
}

// openTerminal switches stdin to raw mode (cfmakeraw): every key reaches the
// pane as typed, including ^C and ^Z, which belong to the program in the pane.
func openTerminal() (terminal, error) {
	fd := os.Stdin.Fd()
	var t syscall.Termios
	if err := termios(fd, ioctlGetTermios, &t); err != nil {
		return nil, err
	}
	u := &unixTerminal{fd: fd, saved: t}
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	if err := termios(fd, ioctlSetTermios, &t); err != nil {
		return nil, err
	}
	return u, nil
}

func (u *unixTerminal) read(p []byte) (int, error) { return os.Stdin.Read(p) }

func (u *unixTerminal) write(p []byte) {
	for len(p) > 0 {
		n, err := os.Stdout.Write(p)
		if err != nil {
			return
		}
		p = p[n:]
	}
}

type winsize struct{ Row, Col, X, Y uint16 }

func (u *unixTerminal) size() (int, int) {
	for _, fd := range []uintptr{os.Stdout.Fd(), u.fd} {
		var ws winsize
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); errno == 0 && ws.Col > 0 && ws.Row > 0 {
			return int(ws.Col), int(ws.Row)
		}
	}
	return 80, 24
}

func (u *unixTerminal) watchResize(fn func(int, int)) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				fn(u.size())
			case <-quit:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(quit)
	}
}

func (u *unixTerminal) restore() { _ = termios(u.fd, ioctlSetTermios, &u.saved) }

// EnableVT is a no-op on Unix, where terminals interpret escape sequences.
func EnableVT() (restore func()) { return func() {} }
