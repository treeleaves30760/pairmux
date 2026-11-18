//go:build windows

package cli

import (
	"os"
	"syscall"
)

// isTerminal reports whether f is a console. A character-device check is not
// enough on Windows: NUL and serial ports are character devices too, and only
// a console (conhost, Windows Terminal, any ConPTY host) has the modes attach
// and the TUI switch. A mintty pipe is not a console, so it is not a terminal
// here.
func isTerminal(f *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}
