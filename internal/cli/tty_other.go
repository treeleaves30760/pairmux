//go:build !windows

package cli

import "os"

// isTerminal reports whether f is an interactive terminal. A terminal is a
// character device, which pipes and regular files are not; the check needs no
// ioctl. The rare false positive (/dev/null is also a char device) is
// harmless: tmux refuses a non-terminal itself, and the built-in attach and the
// TUI fail to switch it to raw mode.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
