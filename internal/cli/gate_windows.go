//go:build windows

package cli

import "errors"

// newStartGate has no Windows implementation: the gate exists for tmux, whose
// pipe-pane attaches after a pane's command has started, and Windows always
// uses the built-in backend, which journals from the first byte.
func newStartGate(string) (*startGate, error) {
	return nil, errors.New("start gate: not needed and not supported on Windows")
}

func (g *startGate) release() error { return nil }
