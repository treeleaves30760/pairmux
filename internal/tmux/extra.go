package tmux

import (
	"strings"

	"github.com/treeleaves30760/pairmux/internal/core"
)

// P2 additions to Client, kept out of tmux.go so the reviewed P1 surface stays
// stable.

// SelectWindow selects the window named name in the pairmux session. Names are
// pre-validated terminal names (no glob metacharacters), so the target matches
// exactly.
func (c *Client) SelectWindow(name string) error {
	_, err := c.run("select-window", "-t", core.SessionName+":"+name)
	return err
}

// HasSession reports whether the pairmux session exists on the socket; any
// error (including no server running) counts as absent.
func (c *Client) HasSession() bool {
	_, err := c.run("has-session", "-t", core.SessionName)
	return err == nil
}

// KillServer kills the tmux server on this client's socket. Intended only for
// doctor's cleanup of throwaway sockets — never the shared pairmux socket.
func (c *Client) KillServer() error {
	_, err := c.run("kill-server")
	return err
}

// PaneTTY returns the terminal device backing a pane. A pane keeps the same
// device for its whole life, so callers record this once rather than asking
// again: prompt classification reads the device's line discipline, and paying a
// tmux round-trip for it on every poll would scale with the number of agents
// watching the terminal.
func (c *Client) PaneTTY(paneID string) (string, error) {
	out, err := c.run("display", "-p", "-t", paneID, "-F", "#{pane_tty}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
