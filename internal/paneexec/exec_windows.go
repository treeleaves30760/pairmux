//go:build windows

package paneexec

import "errors"

// execve has no Windows counterpart: the helper exists for tmux panes, and
// Windows always uses the built-in backend, which sets a pane's environment
// directly.
func execve(string, []string, []string) error {
	return errors.New("not supported on Windows")
}
