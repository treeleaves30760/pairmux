package cli

import (
	"fmt"
	"path/filepath"

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
