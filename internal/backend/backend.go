// Package backend chooses the terminal multiplexer pairmux drives: an external
// tmux server, or pairmux's own built-in server (package muxd).
//
// PAIRMUX_BACKEND selects one explicitly: "tmux" or "builtin". Unset (or
// "auto"), the choice is made per endpoint so that it is stable across the
// commands of one workflow:
//
//  1. Windows always uses the built-in server; tmux does not run there.
//  2. A built-in server already running for the endpoint keeps being used, so
//     terminals created without tmux stay reachable after tmux is installed —
//     unless a tmux server is running for the same endpoint too, in which case
//     tmux, the long-standing default, keeps its live terminals visible.
//  3. Otherwise tmux is used when tmux 3.2 or newer is installed — existing
//     tmux users see no change, and their live terminals stay where they are.
//  4. Otherwise the built-in server, so pairmux works with no tmux (or with a
//     tmux too old to drive) at all.
//
// Every terminal is created with PAIRMUX_BACKEND set to the backend that owns
// it, so a nested pairmux (an agent driving agents) stays on the same one.
package backend

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/muxd"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

// Env is the environment variable that selects a backend explicitly.
const Env = "PAIRMUX_BACKEND"

// Choice is a resolved backend and why it was chosen (shown by doctor).
type Choice struct {
	Kind   string
	Reason string
}

// Seams for tests.
var (
	lookTmux = func() bool {
		_, err := exec.LookPath("tmux")
		return err == nil
	}
	builtinRunning = muxd.Running
	tmuxRunning    = func(socket string) bool { return tmux.New(socket).HasSession() }
	tmuxVersion    = func(socket string) (string, error) { return tmux.New(socket).Version() }
	goos           = runtime.GOOS
)

// Choose resolves the backend for socket from the environment.
func Choose(socket string) (Choice, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(Env))); v {
	case "", "auto":
	case mux.BackendTmux:
		if goos == "windows" {
			return Choice{}, fmt.Errorf("%s=tmux is not available on Windows; unset it or use %s=builtin", Env, Env)
		}
		return Choice{mux.BackendTmux, Env + "=tmux"}, nil
	case mux.BackendBuiltin, "native", "internal":
		return Choice{mux.BackendBuiltin, Env + "=" + v}, nil
	default:
		return Choice{}, fmt.Errorf("unknown %s %q; use tmux, builtin or auto", Env, v)
	}
	if goos == "windows" {
		return Choice{mux.BackendBuiltin, "Windows has no tmux"}, nil
	}
	haveTmux := lookTmux()
	if builtinRunning(socket) {
		if haveTmux && tmuxRunning(socket) {
			return Choice{mux.BackendTmux, "a tmux server and a built-in server are both running for this socket; " +
				"tmux takes precedence (" + Env + "=builtin reaches the built-in terminals)"}, nil
		}
		return Choice{mux.BackendBuiltin, "a built-in server is already running for this socket"}, nil
	}
	if haveTmux {
		v, err := tmuxVersion(socket)
		if err == nil && tooOld(v) {
			return Choice{mux.BackendBuiltin, "tmux " + v + " is older than 3.2"}, nil
		}
		return Choice{mux.BackendTmux, "tmux is installed"}, nil
	}
	return Choice{mux.BackendBuiltin, "tmux is not installed"}, nil
}

// tooOld reports a tmux version token ("3.1c", "next-3.4") below 3.2. A token
// it cannot read (a "master" build, say) is not called too old: tmux stays the
// choice, as it always was, and doctor reports the version.
func tooOld(v string) bool {
	v = strings.TrimPrefix(v, "next-")
	var major, minor int
	if _, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err != nil {
		if _, err := fmt.Sscanf(v, "%d", &major); err != nil {
			return false
		}
	}
	return major < 3 || (major == 3 && minor < 2)
}

// Open returns the multiplexer client for socket per Choose.
func Open(socket string) (mux.Mux, Choice, error) {
	c, err := Choose(socket)
	if err != nil {
		return nil, Choice{}, err
	}
	return OpenKind(c.Kind, socket), c, nil
}

// OpenKind returns a client of the given backend for socket. Nested layers use
// it to stay on their parent's backend.
func OpenKind(kind, socket string) mux.Mux {
	if kind == mux.BackendBuiltin {
		return muxd.New(socket)
	}
	return tmux.New(socket)
}
