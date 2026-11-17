package cli

import (
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/detect"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// treeNode is one terminal somewhere in the layer tree below this
// invocation's endpoint, with everything ls and the TUI show about it.
type treeNode struct {
	Socket  string // endpoint that owns the terminal
	Path    string // address from the root endpoint: "worker/tests"
	Depth   int    // 0 for the root endpoint's own terminals
	Term    state.Terminal
	Status  core.Status
	Pending string // running command, if any
	Lock    int
	Notes   int
	LastMod time.Time
	Child   string // live endpoint this terminal drives, or ""
	Last    bool   // last sibling in its layer (for tree drawing)
}

// walkTree lists the terminals of this invocation's endpoint and, when
// recurse is set, of every live layer below it, depth first so each layer
// follows the terminal that drives it.
func (c *Ctx) walkTree(recurse bool) ([]treeNode, error) {
	var out []treeNode
	err := c.walkLayer(c.Mux, "", 0, recurse, &out)
	return out, err
}

func (c *Ctx) walkLayer(m mux.Mux, prefix string, depth int, recurse bool, out *[]treeNode) error {
	terms, err := state.ListAt(m, c.StateDir)
	if err != nil {
		return err
	}
	for i, t := range terms {
		j := &journal.Journal{Dir: t.Dir}
		st := deriveTerminalStatus(j, t.Alive, t.Mode, t.Meta.Shell == "")
		st, _ = detect.Refine(j, st, t.Mode)
		n := treeNode{
			Socket: m.SocketName(), Path: prefix + t.Name, Depth: depth, Term: t, Status: st,
			Lock: lockHolderPID(t.Dir), Notes: len(readUnseenNotes(j)), Last: i == len(terms)-1,
		}
		if st == core.StatusRunning || st == core.StatusAwaitingInput {
			if pending, ok, err := j.PendingCmd(); err == nil && ok {
				n.Pending = pending.Text
			}
		}
		if mt, ok := j.LastModified(); ok {
			n.LastMod = mt
		}
		n.Child = c.liveChildLayerOf(m.SocketName(), t.Name)
		*out = append(*out, n)
		if recurse && n.Child != "" && depth+1 < maxLayerDepth {
			// A layer that cannot be listed is shown as empty rather than
			// failing the whole tree: its parent's row still says it exists.
			_ = c.walkLayer(c.layer(n.Child), n.Path+"/", depth+1, true, out)
		}
	}
	return nil
}

// row renders a node as an ls row; withPath adds the path (ls --tree).
func (n treeNode) row(withPath bool) output.TerminalRow {
	r := output.TerminalRow{
		Name: n.Term.Name, Status: string(n.Status), Mode: string(n.Term.Mode),
		CurrentCmd: n.Pending, LockHolder: n.Lock, Notes: n.Notes, ChildSocket: n.Child,
	}
	if !n.LastMod.IsZero() {
		r.LastActivity = n.LastMod.UTC().Format(time.RFC3339)
	}
	if withPath {
		r.Path = n.Path
	}
	return r
}
