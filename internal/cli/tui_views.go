package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
)

// This file is the part of the TUI that shows people what the CLI would have
// printed: the selected terminal's activity, its full output history, and the
// result of any pairmux command, each in a scrollable full-screen reader
// instead of scrollback the next redraw wipes.

// Preview views, cycled with tab.
const (
	viewScreen = iota
	viewActivity
	viewCount
)

var viewNames = [viewCount]string{"Screen", "Activity"}

// tuiViewer is the full-screen reader.
type tuiViewer struct {
	id     int
	title  string
	status string
	lines  []string
	off    int
	busy   bool
	// load produces the content; reloadable views rerun it on r.
	load       func() (lines []string, status string)
	reloadable bool
	// bottom starts the reader at the end (histories) rather than the top.
	bottom bool
}

// tuiResult is a reader's content arriving from a background command.
type tuiResult struct {
	id     int
	lines  []string
	status string
}

// openViewer shows a reader. A synchronous one is filled now; an async one
// runs load in the background so a slow command (run, wait) never freezes the
// tree, and arrives through a.results.
func (a *tuiApp) openViewer(v *tuiViewer, async bool) {
	a.viewerSeq++
	v.id = a.viewerSeq
	a.viewer, a.mode = v, modeView
	a.fillViewer(async)
}

func (a *tuiApp) fillViewer(async bool) {
	v := a.viewer
	if !async {
		v.lines, v.status = v.load()
		v.busy = false
		if v.bottom {
			v.off = 1 << 30
		}
		return
	}
	v.busy, v.status = true, "running…"
	id, load := v.id, v.load
	go func() {
		lines, status := load()
		a.results <- tuiResult{id: id, lines: lines, status: status}
	}()
}

// applyResult files a background command's output into its reader, or, if
// the human has already closed that reader, into a flash.
func (a *tuiApp) applyResult(r tuiResult) {
	a.refresh() // the command may have changed the tree
	if a.viewer != nil && a.viewer.id == r.id {
		a.viewer.lines, a.viewer.status, a.viewer.busy = r.lines, r.status, false
		return
	}
	a.setFlash('i', "command finished ("+r.status+") — : reruns it to see the output")
}

func (a *tuiApp) viewKey(k tuiKey) {
	v := a.viewer
	page := max(1, a.bodyHeight()-2)
	switch k.name {
	case "esc":
		a.mode, a.viewer = modeList, nil
	case "ctrl-c":
		a.quit = true
	case "up":
		v.off--
	case "down":
		v.off++
	case "pgup":
		v.off -= page
	case "pgdn":
		v.off += page
	case "home":
		v.off = 0
	case "end":
		v.off = 1 << 30
	case "rune":
		switch k.r {
		case 'q':
			a.mode, a.viewer = modeList, nil
		case 'k':
			v.off--
		case 'j':
			v.off++
		case ' ', 'f':
			v.off += page
		case 'b':
			v.off -= page
		case 'g':
			v.off = 0
		case 'G':
			v.off = 1 << 30
		case 'r':
			if v.reloadable && !v.busy {
				a.fillViewer(!v.bottom)
			}
		case ':':
			a.openCommand(a.current())
		}
	}
	if v.off < 0 {
		v.off = 0
	}
}

func (a *tuiApp) drawViewer(w, h int) []string {
	v := a.viewer
	out := make([]string, 0, h)
	var wrapped []string
	for _, l := range v.lines {
		wrapped = append(wrapped, hardWrap(strings.ReplaceAll(l, "\t", "    "), w-2)...)
	}
	rows := max(1, h-1)
	v.off = max(0, min(v.off, len(wrapped)-rows))
	title := tline{{" " + v.title, stTitle}}
	if v.status != "" {
		st := stDim
		switch {
		case v.status == "exit 0":
			st = stOK
		case strings.HasPrefix(v.status, "exit "):
			st = stErr
		}
		title = append(title, seg{"  " + v.status, st})
	}
	if len(wrapped) > rows {
		title = append(title, seg{fmt.Sprintf("  lines %d–%d of %d", v.off+1, min(len(wrapped), v.off+rows), len(wrapped)), stDim})
	}
	out = append(out, a.render(title, w, ""))
	if len(wrapped) == 0 && !v.busy {
		out = append(out, a.render(tline{{"  (no output)", stDim}}, w, ""))
	}
	for i := v.off; i < len(wrapped) && len(out) < h; i++ {
		out = append(out, a.render(tline{{" " + wrapped[i], ""}}, w, ""))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out
}

func (a *tuiApp) viewerKeyBar() tline {
	keys := [][2]string{{"↑↓", "scroll"}, {"PgUp PgDn", "page"}, {"g G", "top/end"}}
	if a.viewer.reloadable {
		keys = append(keys, [2]string{"r", "rerun"})
	}
	keys = append(keys, [2]string{":", "command"}, [2]string{"esc", "back"})
	var l tline
	for _, k := range keys {
		l = append(l, seg{" " + k[0], stKey}, seg{" " + k[1] + " ", stDim})
	}
	return l
}

// hardWrap cuts s into lines of at most w cells, keeping every character —
// output is read, not reflowed, so spacing and alignment survive.
func hardWrap(s string, w int) []string {
	if w < 1 || strWidth(s) <= w {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	used := 0
	for _, r := range s {
		rw := strWidth(string(r))
		if used+rw > w && used > 0 {
			out = append(out, cur.String())
			cur.Reset()
			used = 0
		}
		cur.WriteRune(r)
		used += rw
	}
	return append(out, cur.String())
}

// --- the selected terminal --------------------------------------------------

func (a *tuiApp) current() *treeNode {
	vis := a.visible()
	if a.sel < 0 || a.sel >= len(vis) {
		return nil
	}
	n := vis[a.sel].node
	return &n
}

// openFull opens the current preview view as a full-screen reader: the whole
// retained output history for Screen, the full event log for Activity.
func (a *tuiApp) openFull(n *treeNode) {
	if n == nil {
		a.setFlash('i', "nothing selected")
		return
	}
	node := *n
	if a.view == viewActivity {
		a.openViewer(&tuiViewer{title: "Activity · " + node.Path, reloadable: true, bottom: true,
			load: func() ([]string, string) { return activityLines(node), "" }}, false)
		return
	}
	a.openViewer(&tuiViewer{title: "Output history · " + node.Path, reloadable: true, bottom: true,
		load: func() ([]string, string) { return historyLines(node), "" }}, false)
}

// historyCap bounds the history reader; log --range reaches anything older.
const historyCap = 1 << 20

func historyLines(n treeNode) []string {
	j := &journal.Journal{Dir: n.Term.Dir}
	raw, start, err := j.TailBytes(historyCap)
	if err != nil {
		return []string{"cannot read the journal: " + err.Error()}
	}
	text := trimTrailingBlank(strings.ReplaceAll(cleanNoTrunc(raw, ""), "\r", ""))
	var out []string
	if start > 0 {
		out = append(out, fmt.Sprintf("… %s of older output not shown — :log %s --range 0:%d reads it", humanBytes(start), n.Path, start), "")
	}
	if text == "" {
		return append(out, "(no output yet)")
	}
	return append(out, strings.Split(text, "\n")...)
}

// activityLines is a person's account of a terminal: what it is, then its
// event log — commands with exit codes and durations, notes (and whether its
// agent has seen them), input sent.
func activityLines(n treeNode) []string {
	_, _, label := statusLook(n.Status)
	out := []string{
		"path      " + n.Path,
		"status    " + label,
		"endpoint  " + n.Socket,
	}
	kind := "program (" + string(n.Term.Mode) + ")"
	if n.Term.Meta.Shell != "" {
		kind = "shell (" + n.Term.Meta.Shell + ")"
	}
	out = append(out, "kind      "+kind)
	if !n.Term.Meta.CreatedAt.IsZero() {
		out = append(out, "created   "+stamp(n.Term.Meta.CreatedAt)+" ("+humanizeAge(time.Since(n.Term.Meta.CreatedAt))+" ago)")
	}
	if n.Pending != "" {
		out = append(out, "running   "+n.Pending)
	}
	if n.Child != "" {
		out = append(out, "drives    layer "+n.Child+" (address its terminals as "+n.Path+"/<name>)")
	}
	if n.Lock > 0 {
		out = append(out, fmt.Sprintf("lock      held by pid %d", n.Lock))
	}
	out = append(out, "journal   "+n.Term.Dir, "")

	evs, err := (&journal.Journal{Dir: n.Term.Dir}).Events()
	if err != nil {
		return append(out, "cannot read the event log: "+err.Error())
	}
	if len(evs) == 0 {
		return append(out, "no activity recorded")
	}
	acted := lastActedOn(evs)
	starts := map[int]time.Time{}
	for _, ev := range evs {
		ts := stamp(ev.TS)
		switch ev.Type {
		case core.EvCreated:
			out = append(out, ts+"  created")
		case core.EvCmdStart:
			starts[ev.CmdID] = ev.TS
			out = append(out, fmt.Sprintf("%s  $ %s   #%d", ts, ev.Text, ev.CmdID))
		case core.EvCmdEnd:
			res := "ended"
			if ev.ExitCode != nil {
				res = fmt.Sprintf("exit %d", *ev.ExitCode)
				if *ev.ExitCode == 0 {
					res += " ✓"
				} else {
					res += " ✕"
				}
			}
			if t, ok := starts[ev.CmdID]; ok && !ev.TS.Before(t) {
				res += " after " + ev.TS.Sub(t).Round(100*time.Millisecond).String()
			}
			out = append(out, fmt.Sprintf("%s    ↳ #%d %s", ts, ev.CmdID, res))
		case core.EvNote:
			seen := ""
			if acted.IsZero() || ev.TS.After(acted) {
				seen = "   (not seen by its agent yet)"
			}
			out = append(out, ts+"  ✉ note: "+ev.Text+seen)
		case core.EvSent:
			out = append(out, ts+"  ⌨ input sent")
		default:
			out = append(out, ts+"  "+string(ev.Type)+" "+ev.Text)
		}
	}
	return out
}

func stamp(t time.Time) string {
	t = t.Local()
	if y, m, d := t.Date(); y == time.Now().Year() && m == time.Now().Month() && d == time.Now().Day() {
		return t.Format("15:04:05")
	}
	return t.Format("Jan 02 15:04")
}

// --- the command line -------------------------------------------------------

// tuiBlocked are commands that cannot run inside the TUI, with what to do.
var tuiBlocked = map[string]string{
	"attach": "select the terminal and press ⏎ to attach",
	"watch":  "this is the live view already",
	"tui":    "you are in it",
	"ui":     "you are in it",
	"mcp":    "mcp serve runs until its client hangs up; start it from a shell",
	"update": "quit (q) and run pairmux update from a shell",
}

// tuiReadOnly commands are safe to rerun with r.
var tuiReadOnly = map[string]bool{"peek": true, "log": true, "ls": true, "list": true,
	"doctor": true, "version": true, "help": true}

const commandTitle = "Run a pairmux command"

// openCommand asks for any pairmux command and shows its readable output in a
// reader. {} stands for the selected terminal's path.
func (a *tuiApp) openCommand(cur *treeNode) {
	sel := ""
	hint := "e.g. log --grep error {}   doctor   prune --dry-run   run {} \"make test\""
	if cur != nil {
		sel = cur.Path
		hint = "{} = " + sel + " · e.g. log {} --cmd 2, doctor, prune --dry-run"
	}
	if a.lastCmd != "" {
		hint += " · ↑ last command"
	}
	a.form = &tuiForm{
		title:  commandTitle,
		fields: []tuiField{{label: "pairmux", hint: hint}},
		recall: a.lastCmd,
		submit: func(v []string) string {
			if v[0] == "" {
				return ""
			}
			return a.runCommand(v[0], sel)
		},
	}
	a.mode = modeForm
}

func (a *tuiApp) runCommand(line, sel string) string {
	args, err := splitCommandLine(line)
	if err != nil {
		return err.Error()
	}
	if len(args) > 0 && args[0] == "pairmux" {
		args = args[1:]
	}
	if strings.Contains(line, "{}") && sel == "" {
		return "{} stands for the selected terminal, and nothing is selected"
	}
	for i := range args {
		args[i] = strings.ReplaceAll(args[i], "{}", sel)
	}
	rest, jsonMode, socket := stripGlobals(args, false, "")
	cmd := ""
	for _, r := range rest {
		if r != "--" {
			cmd = r
			break
		}
	}
	if cmd == "" {
		return "type a command, e.g. doctor"
	}
	if why, blocked := tuiBlocked[cmd]; blocked {
		return cmd + " cannot run here: " + why
	}
	if socket == "" {
		socket = a.c.Mux.SocketName()
	}
	a.lastCmd = line
	shown := "pairmux " + strings.Join(quoteArgs(args), " ")
	load := func() ([]string, string) {
		var buf bytes.Buffer
		sub := &Ctx{Mux: a.c.layer(socket), Choice: a.c.Choice, JSON: jsonMode, StateDir: a.c.StateDir, Stdout: &buf}
		rc, refused := sub.enforceScope(rest)
		if !refused {
			rc = sub.dispatch(rest)
		}
		text := strings.TrimRight(strings.ReplaceAll(buf.String(), "\r", ""), "\n")
		return strings.Split(text, "\n"), fmt.Sprintf("exit %d", rc)
	}
	a.openViewer(&tuiViewer{title: "$ " + shown, load: load, reloadable: tuiReadOnly[cmd]}, true)
	return ""
}

// splitCommandLine splits a line the way a POSIX shell would for plain words:
// single quotes are literal, double quotes allow \" and \\, and a backslash
// outside quotes escapes the next character. No expansion of any kind.
func splitCommandLine(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	var quote rune
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				esc = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			esc, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unclosed " + string(quote) + " quote")
	}
	if esc {
		cur.WriteRune('\\')
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'\\") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return out
}

func viewerHint(v *tuiViewer) string {
	switch {
	case v == nil:
		return ""
	case v.busy:
		return "running in the background — esc goes back to the tree; the result waits here"
	case v.reloadable:
		return "r reruns it · esc goes back to the tree"
	}
	return "esc goes back to the tree"
}
