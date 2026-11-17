package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// This file holds the TUI's actions: each one runs the same pairmux
// subcommand an agent would, in-process against the right layer.

// subCtx is a Ctx for one in-process subcommand against socket, writing to w.
// It only reads a.c, so a background command (the : line) may build one too.
func (a *tuiApp) subCtx(socket string, jsonMode bool, w io.Writer) *Ctx {
	return &Ctx{Mux: a.c.layer(socket), Choice: a.c.Choice, JSON: jsonMode, StateDir: a.c.StateDir, Stdout: w}
}

// exec runs one pairmux subcommand in-process against socket and returns its
// envelope.
func (a *tuiApp) exec(socket string, args ...string) output.Envelope {
	var buf bytes.Buffer
	a.subCtx(socket, true, &buf).dispatch(args)
	var e output.Envelope
	line := bytes.TrimSpace(buf.Bytes())
	if i := bytes.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	if err := json.Unmarshal(line, &e); err != nil {
		e = output.Envelope{Error: &output.ErrInfo{Code: output.CodeInternal, Message: strings.TrimSpace(buf.String())}}
	}
	return e
}

func envErr(e output.Envelope) string {
	if e.Error == nil {
		return ""
	}
	msg := e.Error.Message
	if e.Error.Hint != "" {
		msg += " — " + e.Error.Hint
	}
	return msg
}

func (a *tuiApp) doAttach(n *treeNode) {
	if n == nil {
		a.setFlash('i', "nothing to attach to — press n to create a terminal")
		return
	}
	if !n.Term.Alive {
		a.setFlash('e', n.Path+" is dead — d deletes its journal, x deletes every dead one")
		return
	}
	a.pendingAttach = n
}

// layerSocket maps a layer path ("" for this endpoint, "worker/sub" for the
// layer sub drives) to its endpoint.
func (a *tuiApp) layerSocket(path string) (string, error) {
	socket := a.c.Mux.SocketName()
	if path == "" {
		return socket, nil
	}
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if !state.ValidName(seg) {
			return "", fmt.Errorf("invalid layer path %q", path)
		}
		socket = state.ChildSocket(socket, seg)
	}
	return socket, nil
}

// openNew asks for a new terminal. In the top layer by default; with child
// set, in the layer the selected terminal drives — a sub-agent of it.
func (a *tuiApp) openNew(cur *treeNode, child bool) {
	layer := ""
	if child {
		if cur == nil {
			a.setFlash('e', "select the terminal that should own the new one")
			return
		}
		layer = cur.Path
	}
	wd, _ := os.Getwd()
	title := "New terminal"
	if layer != "" {
		title = "New terminal under " + layer
	}
	a.form = &tuiForm{
		title: title,
		fields: []tuiField{
			{label: "Name", hint: "blank = auto (t1, t2, …)"},
			{label: "Command", hint: "blank = interactive shell; e.g. claude, codex, python3"},
			{label: "Directory", value: wd},
			{label: "Layer", value: layer, hint: "path of the owning terminal; blank = top"},
		},
		submit: func(v []string) string {
			socket, err := a.layerSocket(v[3])
			if err != nil {
				return err.Error()
			}
			args := []string{"new"}
			if v[0] != "" {
				args = append(args, "--name", v[0])
			}
			if v[1] != "" {
				args = append(args, "--cmd", v[1])
			}
			if v[2] != "" {
				args = append(args, "--cwd", v[2])
			}
			a.setFlash('i', "creating…")
			a.draw()
			e := a.exec(socket, args...)
			if msg := envErr(e); msg != "" {
				a.flash = ""
				return msg
			}
			path := e.Terminal
			if v[3] != "" {
				path = strings.Trim(v[3], "/") + "/" + e.Terminal
				// make the new row visible
				for p := strings.Trim(v[3], "/"); p != ""; {
					delete(a.collapsed, p)
					i := strings.LastIndex(p, "/")
					if i < 0 {
						break
					}
					p = p[:i]
				}
			}
			a.refresh()
			a.selectPath(path)
			a.setFlash('o', "created "+path+" — ⏎ to attach")
			return ""
		},
	}
	a.mode = modeForm
}

func (a *tuiApp) descendants(n treeNode) int {
	count := 0
	for _, o := range a.nodes {
		if strings.HasPrefix(o.Path, n.Path+"/") {
			count++
		}
	}
	return count
}

// doDelete kills a live terminal (keeping its journal) or deletes a dead
// one's retained journal. Both ask first.
func (a *tuiApp) doDelete(n *treeNode) {
	if n == nil {
		return
	}
	node := *n
	below := a.descendants(node)
	if node.Term.Alive {
		msg := fmt.Sprintf("Kill %s?", node.Path)
		if below > 0 {
			msg = fmt.Sprintf("Kill %s and the %d terminal(s) below it?", node.Path, below)
		}
		if node.Status == core.StatusRunning || node.Status == core.StatusAwaitingInput {
			msg += " It is " + string(node.Status) + "; unsaved work in it is lost."
		}
		a.ask("Kill terminal", msg+" The journal is kept (x deletes it later).", func() {
			e := a.exec(node.Socket, "kill", node.Term.Name)
			if m := envErr(e); m != "" {
				a.setFlash('e', "kill: "+m)
			} else {
				a.setFlash('o', "killed "+node.Path)
			}
			a.refresh()
		})
		return
	}
	msg := fmt.Sprintf("Delete the retained journal of dead terminal %s? Its history cannot be recovered.", node.Path)
	if node.Child != "" {
		msg = fmt.Sprintf("%s is dead but still drives %d terminal(s). Tear them down and delete its journal?", node.Path, below)
	}
	a.ask("Delete terminal", msg, func() {
		if node.Child != "" {
			if m := envErr(a.exec(node.Socket, "kill", node.Term.Name)); m != "" {
				a.setFlash('e', "kill: "+m)
				a.refresh()
				return
			}
		}
		e := a.exec(node.Socket, "prune", node.Term.Name)
		if m := envErr(e); m != "" {
			a.setFlash('e', "delete: "+m)
		} else {
			a.setFlash('o', "deleted "+node.Path)
		}
		a.refresh()
	})
}

// doPruneDead deletes the retained journals of every dead terminal in the
// tree, one prune sweep per layer that has any.
func (a *tuiApp) doPruneDead() {
	var sockets []string
	seen := map[string]bool{}
	dead := 0
	for _, n := range a.nodes {
		if n.Status == core.StatusDead {
			dead++
			if !seen[n.Socket] {
				seen[n.Socket] = true
				sockets = append(sockets, n.Socket)
			}
		}
	}
	if dead == 0 {
		a.setFlash('i', "no dead terminals to delete")
		return
	}
	a.ask("Delete stale terminals",
		fmt.Sprintf("Delete the retained journals of %d dead terminal(s) across %d layer(s)? Live terminals are not touched.", dead, len(sockets)),
		func() {
			var errs, report []string
			for _, s := range sockets {
				e := a.exec(s, "prune")
				if m := envErr(e); m != "" {
					errs = append(errs, m)
					report = append(report, "layer "+s+": "+m, "")
					continue
				}
				report = append(report, "layer "+s+":")
				for _, l := range strings.Split(e.Output, "\n") {
					report = append(report, "  "+l)
				}
				report = append(report, "")
			}
			a.refresh()
			status := "exit 0"
			if len(errs) > 0 {
				status = "exit 1"
				a.setFlash('e', "prune: "+strings.Join(errs, "; "))
			} else {
				a.setFlash('o', fmt.Sprintf("deleted %d dead terminal(s)", dead))
			}
			a.openViewer(&tuiViewer{title: "Prune dead terminals",
				load: func() ([]string, string) { return report, status }}, false)
		})
}

func (a *tuiApp) openNote(n *treeNode) {
	if n == nil {
		return
	}
	node := *n
	a.form = &tuiForm{
		title:  "Note for the agent driving " + node.Path,
		fields: []tuiField{{label: "Message", hint: "the agent sees it in its next reply; never put secrets here"}},
		submit: func(v []string) string {
			if v[0] == "" {
				a.setFlash('i', "empty note not sent")
				return ""
			}
			if m := envErr(a.exec(node.Socket, "note", node.Term.Name, v[0])); m != "" {
				return m
			}
			a.setFlash('o', "note left on "+node.Path)
			a.refresh()
			return ""
		},
	}
	a.mode = modeForm
}

func (a *tuiApp) openSend(n *treeNode) {
	if n == nil || !n.Term.Alive {
		a.setFlash('e', "select a live terminal to send input to")
		return
	}
	node := *n
	a.form = &tuiForm{
		title:  "Send a line to " + node.Path,
		fields: []tuiField{{label: "Text", hint: "typed into the program, then Enter; for secrets attach instead (⏎)"}},
		submit: func(v []string) string {
			if m := envErr(a.exec(node.Socket, "send", node.Term.Name, "--text", v[0], "--enter")); m != "" {
				return m
			}
			a.setFlash('o', "sent to "+node.Path)
			a.refresh()
			return ""
		},
	}
	a.mode = modeForm
}

func (a *tuiApp) ask(title, msg string, yes func()) {
	a.confirm = &tuiConfirm{title: title, msg: msg, yes: yes}
	a.mode = modeConfirm
}

func (a *tuiApp) setFlash(kind byte, msg string) {
	a.flash, a.flashKind, a.flashAt = msg, kind, time.Now()
}
