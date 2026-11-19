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

// RawPoll switches stdin to raw mode for an interactive UI (pairmux tui) and
// returns the function that restores it. Unlike attach's raw mode, reads are
// polled: VMIN=0, VTIME=1 makes a read return after at most 100ms with no
// input, so the UI's reader can be parked between reads and the terminal
// handed whole to a child process — an attach — without the reader stealing
// its keystrokes. Output processing is kept on so "\n" still returns the
// carriage, and signals are off so Ctrl-C reaches the UI as a key.
func RawPoll() (restore func(), err error) {
	fd := os.Stdin.Fd()
	var saved syscall.Termios
	if err := termios(fd, ioctlGetTermios, &saved); err != nil {
		return nil, err
	}
	t := saved
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 0
	t.Cc[syscall.VTIME] = 1
	if err := termios(fd, ioctlSetTermios, &t); err != nil {
		return nil, err
	}
	return func() { _ = termios(fd, ioctlSetTermios, &saved) }, nil
}

// PollRead reads input from a terminal RawPoll set up. It returns within about
// 100ms whether or not a key arrived (0 bytes and io.EOF when none did), so the
// caller can stop reading between calls without leaving a read pending.
func PollRead(p []byte) (int, error) { return os.Stdin.Read(p) }

// TermSize reports the size of the terminal on stdout (else stdin), or 80x24.
func TermSize() (cols, rows int) {
	return (&unixTerminal{fd: os.Stdin.Fd()}).size()
}

// WatchResize calls fn whenever the terminal is resized, until stop is called.
func WatchResize(fn func()) (stop func()) {
	return (&unixTerminal{fd: os.Stdin.Fd()}).watchResize(func(int, int) { fn() })
}
