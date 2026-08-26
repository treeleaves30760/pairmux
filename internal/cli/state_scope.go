package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

const terminalNameHint = `names match ^[a-z0-9][a-z0-9_-]{0,31}$`
const socketNameHint = `socket names match ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`

// rejectInvalidSocket is the global boundary for tmux's -L value. tmux itself
// accepts ../ in socket names, so every handler that can touch state or tmux
// calls this before doing either. Client.run repeats the check as defense in
// depth for future call sites.
func (c *Ctx) rejectInvalidSocket() (rc int, rejected bool) {
	if tmux.ValidSocketName(c.Tmux.Socket) {
		return 0, false
	}
	return c.fail(output.CodeBadArgs, fmt.Sprintf("invalid socket name %q", c.Tmux.Socket), socketNameHint), true
}

// rejectInvalidTerminalName is the common boundary for every command that
// accepts a terminal name. Callers invoke it immediately after positional
// parsing, before any state or tmux operation.
func (c *Ctx) rejectInvalidTerminalName(name string) (rc int, rejected bool) {
	if state.ValidName(name) {
		return 0, false
	}
	return c.fail(output.CodeBadArgs, fmt.Sprintf("invalid name %q", name), terminalNameHint), true
}

// namespaceDir is this invocation's socket-specific state directory. StateDir
// remains the user-configured root so --socket and PAIRMUX_SOCKET take the same
// path even though global flag parsing lives outside the state package.
func (c *Ctx) namespaceDir() string {
	return state.SocketDir(c.StateDir, c.Tmux.Socket)
}

func (c *Ctx) terminalDir(name string) string {
	return filepath.Join(c.namespaceDir(), name)
}

// liveChildLayer returns the endpoint of the nested pairmux layer a terminal is
// driving, or "" when it is driving none.
//
// The cheap half comes first on purpose: a terminal that has never created a
// child has no namespace directory for one, which is an os.Stat rather than a
// tmux round-trip, and that is nearly every terminal in nearly every listing.
// Only a terminal that did create children pays for the liveness check, which
// is what distinguishes a layer still running from the directory left behind by
// one that has been killed.
func (c *Ctx) liveChildLayer(name string) string {
	socket := state.ChildSocket(c.Tmux.Socket, name)
	if !isDir(state.SocketDir(c.StateDir, socket)) {
		return ""
	}
	if !tmux.New(socket).HasSession() {
		return ""
	}
	return socket
}

// maxLayerDepth bounds how far killLayers will descend. Endpoint names strictly
// lengthen with depth (state.ChildSocket), so a cycle is not reachable; the cap
// is there so a corrupted state tree cannot turn a kill into a runaway.
const maxLayerDepth = 16

// killLayers tears down the nested pairmux layers a terminal owns, deepest
// first, and returns the endpoints it destroyed.
//
// This is part of killing the terminal, not an extra. A child layer exists only
// to serve one terminal, and its panes are the sub-agents that terminal was
// driving: left behind they keep running, unattached and unlisted at every
// layer a human is likely to look at, which for an agent means it keeps
// spending. Each layer is its own tmux server, so a grandchild does not go with
// its parent's server — the descent has to be explicit.
func (c *Ctx) killLayers(socket, name string, depth int) []string {
	if depth >= maxLayerDepth {
		return nil
	}
	child := state.ChildSocket(socket, name)
	if !isDir(state.SocketDir(c.StateDir, child)) {
		return nil
	}
	cl := tmux.New(child)
	if !cl.HasSession() {
		return nil
	}
	var killed []string
	if panes, err := cl.ListManaged(); err == nil {
		for _, p := range panes {
			if state.ValidName(p.Name) {
				killed = append(killed, c.killLayers(child, p.Name, depth+1)...)
			}
		}
	}
	_ = cl.KillServer() // its own endpoint, owned by this terminal alone
	return append(killed, child)
}

// layersNext renders killLayers' result as a line for an envelope's next steps,
// or "" when nothing nested was running.
func layersNext(killed []string) string {
	if len(killed) == 0 {
		return ""
	}
	return fmt.Sprintf("also tore down %d nested layer(s) it was driving: %s",
		len(killed), strings.Join(killed, ", "))
}
