package cli

import "os"

// startGate holds a program terminal's command until its journal is wired.
//
// tmux starts a pane's command as soon as the window exists, which is strictly
// before pipe-pane can be attached to it. Whatever the program prints in that
// window is not journalled and cannot be recovered, so a program whose first
// act is to prompt — ssh, a login flow, anything asking a question up front —
// can be invisible to pairmux for its whole life. A FIFO the pane blocks on
// turns that race into an ordering: the command cannot start until the caller
// says so, and the caller says so once the journal is capturing.
//
// Only the tmux backend needs it; the built-in backend opens the journal before
// it starts the program (mux.Mux.AtomicJournal).
type startGate struct{ path string }

func (g *startGate) Path() string {
	if g == nil {
		return ""
	}
	return g.path
}

// abandon drops the FIFO without releasing it, for a creation that failed
// before the pane was usable. The pane is killed by the caller's cleanup; the
// holder's read ends when its terminal goes.
func (g *startGate) abandon() {
	if g != nil {
		_ = os.Remove(g.path)
	}
}
