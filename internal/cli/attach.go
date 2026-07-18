package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// isTTY reports whether pairmux's stdout is an interactive terminal. A terminal
// is a character device, which pipes and regular files are not; the check is
// intentionally dependency-free (no ioctl, no build tags) so it compiles the
// same on darwin and linux. It is a package var so tests can override it. The
// rare false positive (/dev/null is also a char device) is harmless: attach
// then execs tmux, which itself refuses a non-terminal.
var isTTY = func() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// insideTmux reports whether we are already running inside a tmux client (the
// tmux server exports $TMUX to its panes). Package var for test override.
var insideTmux = func() bool {
	return os.Getenv("TMUX") != ""
}

// cmdAttach hands the human a live tmux client on the pairmux session, replacing
// the pairmux process with tmux via exec. It refuses when the current process is
// a poor place to take over the screen (already nested in tmux, or stdout is not
// a terminal) and, when a name is given, focuses that window first.
func (c *Ctx) cmdAttach(args []string) int {
	pos, err := parseFlags(args, flagSpec{})
	if err != nil {
		return c.usage("pairmux attach [name]", err.Error())
	}
	if len(pos) > 1 {
		return c.usage("pairmux attach [name]", "unexpected argument "+pos[1])
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}

	var name string
	if len(pos) > 0 {
		name = pos[0]
		if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
			return rc
		}
	}

	// Environment refusals: politely decline rather than nest tmux or exec into
	// a pipe. Both are E_BAD_ARGS with a concrete escape hatch.
	if insideTmux() {
		return c.fail(output.CodeBadArgs,
			"already inside tmux ($TMUX is set); attaching here would nest tmux",
			"from your tmux session run: tmux switch-client -t "+core.SessionName)
	}
	if !isTTY() {
		return c.fail(output.CodeBadArgs,
			"attach needs an interactive terminal, but stdout is not a tty",
			"run pairmux attach from an interactive shell")
	}

	if name != "" {
		term, err := state.ResolveAt(c.Tmux, c.StateDir, name)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return c.noTerminal(name)
			}
			return c.tmuxErr(err)
		}
		if !term.Alive {
			return c.fail(output.CodeDead,
				fmt.Sprintf("terminal %q is dead", name),
				"journal retained at "+term.Dir+" — pairmux log "+name)
		}
		// Focus the requested window so the attach lands on it.
		if err := c.Tmux.SelectWindow(name); err != nil {
			return c.tmuxErr(err)
		}
	} else if !c.Tmux.HasSession() {
		return c.fail(output.CodeNoTerminal, "no pairmux session to attach to", "pairmux new")
	}

	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return c.fail(output.CodeTmux, "tmux not found on PATH: "+err.Error(), "install tmux >= 3.2")
	}

	// Replace this process with tmux; on success nothing below runs and the
	// human sees a full-screen tmux (so we deliberately print nothing first).
	if err := syscall.Exec(tmuxPath, c.Tmux.AttachArgv(), os.Environ()); err != nil {
		return c.fail(output.CodeTmux, "exec tmux attach: "+err.Error(), "pairmux ls")
	}
	return 0 // unreachable: a successful Exec has replaced this process
}
