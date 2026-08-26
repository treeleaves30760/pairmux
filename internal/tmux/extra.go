package tmux

import (
	"strconv"
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

// KillServer kills the tmux server on this client's socket. Two callers only:
// doctor's cleanup of throwaway sockets, and the teardown of a nested layer's
// endpoint, which belongs to exactly one terminal and dies with it. Never the
// endpoint the caller is itself working on.
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

// PaneAudience reports whether a human is currently watching a pane: how many
// clients are attached to its session, and whether its window is the one they
// are looking at.
//
// It is one display call, not two, because send pays for it on every delivery.
// The question matters because no lock pairmux can take reaches a human at a
// keyboard: their keystrokes go straight to the pane, so the most an agent can
// be told is that it is sharing the input line with someone.
func (c *Client) PaneAudience(paneID string) (attached int, active bool, err error) {
	out, err := c.run("display", "-p", "-t", paneID, "-F", "#{session_attached} #{window_active}")
	if err != nil {
		return 0, false, err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		return 0, false, nil
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false, nil
	}
	return n, fields[1] == "1", nil
}
