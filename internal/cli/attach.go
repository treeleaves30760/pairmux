package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/muxd"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// isTTY reports whether pairmux's stdout is an interactive terminal. A terminal
// is a character device, which pipes and regular files are not; the check is
// intentionally dependency-free (no ioctl, no build tags) so it compiles the
// same everywhere. It is a package var so tests can override it. The rare
// false positive (/dev/null is also a char device) is harmless: tmux refuses a
// non-terminal itself, and the built-in attach fails to switch it to raw mode.
var isTTY = func() bool { return isCharDevice(os.Stdout) }

// stdinIsTTY is isTTY for stdin. Only attach needs it — it reads keystrokes —
// so watch, which only draws, keeps working with a piped stdin.
var stdinIsTTY = func() bool { return isCharDevice(os.Stdin) }

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
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

// insideBuiltinPane returns the "<socket>/<pane>" identity of the built-in pane
// this process runs in, or "".
var insideBuiltinPane = func() string {
	return os.Getenv(muxd.BuiltinPaneEnv)
}

// builtinAttach runs the built-in attach client. Package var for test override.
var builtinAttach = muxd.Attach

// cmdAttach hands the human a live view of a terminal they can type into. On
// tmux it replaces the pairmux process with a tmux client on the pairmux
// session; on the built-in backend it attaches this terminal to the pane
// directly (detach with Ctrl-b d, as in tmux). It refuses when the current
// process is a poor place to take over the screen.
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
	builtin := c.Mux.Backend() == mux.BackendBuiltin

	// Environment refusals: politely decline rather than nest tmux or take
	// over a pipe. Both are E_BAD_ARGS with a concrete escape hatch.
	if !builtin && insideTmux() {
		return c.fail(output.CodeBadArgs,
			"already inside tmux ($TMUX is set); attaching here would nest tmux",
			"detach from the current tmux client, then run pairmux attach from the outer shell (or use another terminal)")
	}
	if !isTTY() || !stdinIsTTY() {
		return c.fail(output.CodeBadArgs,
			"attach needs an interactive terminal, but stdin or stdout is not a tty",
			"run pairmux attach from an interactive shell")
	}

	if name == "" && builtin {
		// The built-in server has no session for a client to land in; attach
		// targets one terminal, so an unambiguous one is chosen for the human.
		picked, rc, failed := c.pickAttachTarget()
		if failed {
			return rc
		}
		name = picked
	}

	var paneID string
	if name != "" {
		term, err := state.ResolveAt(c.Mux, c.StateDir, name)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return c.noTerminal(name)
			}
			return c.muxErr(err)
		}
		if !term.Alive {
			return c.fail(output.CodeDead,
				fmt.Sprintf("terminal %q is dead", name),
				"journal retained at "+term.Dir+" — pairmux log "+name)
		}
		paneID = term.PaneID
		if !builtin {
			// Focus the requested window so the attach lands on it.
			if err := c.Mux.SelectWindow(name); err != nil {
				return c.muxErr(err)
			}
		}
	} else if !c.Mux.HasSession() {
		return c.fail(output.CodeNoTerminal, "no pairmux session to attach to", "pairmux new")
	}

	if !builtin {
		return c.execTmuxAttach()
	}
	if insideBuiltinPane() == c.Mux.SocketName()+"/"+paneID {
		return c.fail(output.CodeBadArgs,
			fmt.Sprintf("this shell is running inside terminal %q; attaching it to itself would loop", name),
			"run pairmux attach from another terminal")
	}
	res, err := builtinAttach(c.Mux.SocketName(), paneID)
	if err != nil {
		return c.muxErr(err)
	}
	if res.Detached {
		fmt.Fprintf(c.Stdout, "[detached from %s] tell the agent what you did: pairmux note %s \"...\"\n", name, name)
	} else if res.Message != "" {
		fmt.Fprintf(c.Stdout, "[%s]\n", res.Message)
	}
	return 0
}

// pickAttachTarget chooses the terminal a name-less built-in attach opens:
// the only live one. Several live terminals are listed for the human to pick.
func (c *Ctx) pickAttachTarget() (string, int, bool) {
	terms, err := state.ListAt(c.Mux, c.StateDir)
	if err != nil {
		return "", c.muxErr(err), true
	}
	var live []string
	for _, t := range terms {
		if t.Alive {
			live = append(live, t.Name)
		}
	}
	switch len(live) {
	case 0:
		return "", c.fail(output.CodeNoTerminal, "no live terminal to attach to", "pairmux new"), true
	case 1:
		return live[0], 0, false
	}
	return "", c.fail(output.CodeBadArgs,
		"several terminals are running; name one: "+strings.Join(live, ", "),
		"pairmux attach "+live[0]), true
}
