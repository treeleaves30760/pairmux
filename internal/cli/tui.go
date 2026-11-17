package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/muxd"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
	"github.com/treeleaves30760/pairmux/internal/vt"
)

// cmdTUI runs the interactive session manager: the whole layer tree below this
// endpoint, a live preview of the selected terminal, and single keys to
// attach, create, kill, delete stale journals, leave a note or send a line.
// It is the human's console over a fleet of agents — `watch` with hands.
//
// Every action is the same subcommand an agent would run, executed in-process
// against the right layer and read back as an envelope, so the TUI cannot drift
// from the CLI's rules (name grammar, busy locks, layer teardown, prune
// safety). Only attach leaves the process, because it takes over the terminal.
func (c *Ctx) cmdTUI(args []string) int {
	var intervalS string
	if _, err := parseFlags(args, flagSpec{vals: map[string]*string{"interval": &intervalS}}); err != nil {
		return c.usage("pairmux tui [--interval 1s]", err.Error())
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	interval := time.Second
	if intervalS != "" {
		d, err := parseInterval(intervalS)
		if err != nil {
			return c.usage("pairmux tui [--interval 1s]", "bad --interval: "+err.Error())
		}
		interval = d
	}
	if !isTTY() || !stdinIsTTY() {
		return c.fail(output.CodeBadArgs,
			"tui needs an interactive terminal, but stdin or stdout is not a tty",
			"run pairmux tui from an interactive shell; agents use pairmux ls --tree")
	}
	app := &tuiApp{c: c, interval: interval, collapsed: map[string]bool{},
		noColor: os.Getenv("NO_COLOR") != "", results: make(chan tuiResult, 4)}
	if err := app.run(); err != nil {
		if errors.Is(err, errTUIUnsupported) {
			return c.fail(output.CodeBadArgs, err.Error(), "pairmux watch")
		}
		return c.fail(output.CodeInternal, "tui: "+err.Error(), "pairmux ls --tree")
	}
	return 0
}

var errTUIUnsupported = errors.New("unsupported")

// NoTUIEnv set to anything non-empty keeps a bare ls a listing everywhere.
const NoTUIEnv = "PAIRMUX_NO_TUI"

// agentShellEnv are variables agent CLIs export to the shells their tools run
// in. Some of those shells get a pty, which would otherwise look exactly like a
// person at a terminal.
var agentShellEnv = []string{"CLAUDECODE", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "GEMINI_CLI", "OPENCODE"}

// humanAtTerminal reports whether this invocation is a person at an
// interactive terminal, the one case where a bare ls opens the session manager
// instead of printing. It errs toward printing: an agent that got a TUI it
// cannot drive would hang, a person who got a table loses nothing.
func (c *Ctx) humanAtTerminal() bool {
	// The TUI needs a unix tty (muxd.RawPoll); on Windows ls stays a table.
	if runtime.GOOS == "windows" || c.JSON || c.Stdout != os.Stdout || !isTTY() || !stdinIsTTY() {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	// Inside a pairmux terminal the caller is presumably the agent it hosts.
	for _, k := range append([]string{NoTUIEnv, ScopeEnv, NameEnv}, agentShellEnv...) {
		if os.Getenv(k) != "" {
			return false
		}
	}
	return true
}

type tuiMode int

const (
	modeList tuiMode = iota
	modeForm
	modeConfirm
	modeHelp
	modeFilter
	modeView
)

type tuiField struct {
	label, value, hint string
}

type tuiForm struct {
	title  string
	fields []tuiField
	focus  int
	err    string
	// submit acts on the values; a non-empty return keeps the form open with
	// that error, so a typo costs one correction, not the whole form.
	submit func(vals []string) string
	// recall is what ↑ puts back in a one-field form: the last command.
	recall string
}

type tuiConfirm struct {
	title, msg string
	yes        func()
}

type tuiApp struct {
	c        *Ctx
	interval time.Duration
	noColor  bool

	nodes     []treeNode
	listErr   error
	selPath   string
	sel       int
	offset    int
	collapsed map[string]bool
	filter    string

	mode    tuiMode
	form    *tuiForm
	confirm *tuiConfirm

	flash     string
	flashKind byte // 'i' info, 'o' ok, 'e' error
	flashAt   time.Time

	preview     []string
	previewMeta string
	view        int // viewScreen or viewActivity

	viewer    *tuiViewer
	viewerSeq int
	results   chan tuiResult
	lastCmd   string

	w, h          int
	quit          bool
	pendingAttach *treeNode

	restoreTTY func()
	reader     *ttyReader
	out        io.Writer
}

// --- lifecycle -------------------------------------------------------------

const (
	tuiEnter = "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J"
	tuiLeave = "\x1b[0m\x1b[?25h\x1b[?1049l"
)

func (a *tuiApp) run() (err error) {
	a.out = a.c.Stdout
	restore, err := muxd.RawPoll()
	if err != nil {
		if errors.Is(err, muxd.ErrNoTUI) {
			return fmt.Errorf("%w: %v", errTUIUnsupported, err)
		}
		return err
	}
	a.restoreTTY = restore
	io.WriteString(a.out, tuiEnter)
	defer func() {
		io.WriteString(a.out, tuiLeave)
		a.restoreTTY()
		if r := recover(); r != nil {
			panic(r)
		}
	}()

	a.reader = newTTYReader()
	go a.reader.loop()

	resized := make(chan struct{}, 1)
	stopResize := muxd.WatchResize(func() {
		select {
		case resized <- struct{}{}:
		default:
		}
	})
	defer stopResize()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)

	a.w, a.h = muxd.TermSize()
	a.refresh()
	tick := time.NewTicker(a.interval)
	defer tick.Stop()
	for !a.quit {
		a.draw()
		select {
		case b := <-a.reader.out:
			for _, k := range parseKeys(b) {
				a.handleKey(k)
			}
		case r := <-a.results:
			a.applyResult(r)
		case <-tick.C:
			a.refresh()
		case <-resized:
			a.w, a.h = muxd.TermSize()
		case <-sig:
			a.quit = true
		}
		if a.pendingAttach != nil {
			n := *a.pendingAttach
			a.pendingAttach = nil
			a.attach(n)
			a.w, a.h = muxd.TermSize()
			a.refresh()
		}
	}
	return nil
}

// attach hands the whole terminal to `pairmux attach` in a child process and
// takes it back when the human detaches. The reader is parked first so it
// cannot swallow keystrokes meant for the attached pane.
func (a *tuiApp) attach(n treeNode) {
	a.reader.park()
	io.WriteString(a.out, tuiLeave)
	a.restoreTTY()

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	cmd := exec.Command(exe, "--socket", n.Socket, "attach", n.Term.Name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	env := os.Environ()
	if a.c.Mux.Backend() == mux.BackendTmux && insideTmux() {
		// attach refuses to nest tmux for agents; a human who asked for it
		// from the TUI gets the nested client, and the hint on how to leave.
		env = withoutEnv(env, "TMUX")
	}
	cmd.Env = env
	runErr := cmd.Run()

	if restore, err := muxd.RawPoll(); err == nil {
		a.restoreTTY = restore
	}
	io.WriteString(a.out, tuiEnter)
	a.reader.resume()
	switch {
	case runErr != nil:
		a.setFlash('e', fmt.Sprintf("attach %s failed: %v", n.Path, runErr))
	default:
		a.setFlash('o', fmt.Sprintf("back from %s — press m to tell its agent what you did", n.Path))
	}
}

func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// --- input -------------------------------------------------------------

// ttyReader reads stdin on its own goroutine. Reads are polled (RawPoll), so
// park returns within about 100ms with the reader idle and holding nothing.
type ttyReader struct {
	out     chan []byte
	parkReq chan chan struct{}
	resumed chan struct{}
}

func newTTYReader() *ttyReader {
	return &ttyReader{out: make(chan []byte, 64), parkReq: make(chan chan struct{}), resumed: make(chan struct{})}
}

func (r *ttyReader) loop() {
	buf := make([]byte, 512)
	for {
		select {
		case ack := <-r.parkReq:
			close(ack)
			<-r.resumed
			continue
		default:
		}
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			b := append([]byte(nil), buf[:n]...)
			select {
			case r.out <- b:
			case ack := <-r.parkReq:
				close(ack)
				<-r.resumed
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func (r *ttyReader) park() {
	ack := make(chan struct{})
	r.parkReq <- ack
	<-ack
	for len(r.out) > 0 { // keys typed before the attach belong to the TUI, not the pane
		<-r.out
	}
}

func (r *ttyReader) resume() { r.resumed <- struct{}{} }

type tuiKey struct {
	name string // "rune" for printable input
	r    rune
}

// parseKeys decodes one read's worth of terminal input into keys. A read
// carries whole escape sequences in practice; an ESC with nothing recognisable
// after it is the Escape key.
func parseKeys(b []byte) []tuiKey {
	var keys []tuiKey
	for len(b) > 0 {
		if b[0] == 0x1b {
			if len(b) == 1 {
				keys = append(keys, tuiKey{name: "esc"})
				b = b[1:]
				continue
			}
			if b[1] == '[' || b[1] == 'O' {
				i := 2
				for i < len(b) && (b[i] >= '0' && b[i] <= '9' || b[i] == ';') {
					i++
				}
				if i < len(b) {
					seq := string(b[2 : i+1])
					b = b[i+1:]
					if name := csiKeys[seq]; name != "" {
						keys = append(keys, tuiKey{name: name})
					}
					continue
				}
				b = nil
				continue
			}
			// Alt-<key> or a lone ESC followed by typing: treat as ESC.
			keys = append(keys, tuiKey{name: "esc"})
			b = b[1:]
			continue
		}
		switch b[0] {
		case '\r', '\n':
			keys = append(keys, tuiKey{name: "enter"})
		case 0x7f, 0x08:
			keys = append(keys, tuiKey{name: "backspace"})
		case '\t':
			keys = append(keys, tuiKey{name: "tab"})
		case 0x03:
			keys = append(keys, tuiKey{name: "ctrl-c"})
		case 0x15:
			keys = append(keys, tuiKey{name: "ctrl-u"})
		default:
			if b[0] < 0x20 {
				b = b[1:]
				continue
			}
			r, size := decodeRune(b)
			keys = append(keys, tuiKey{name: "rune", r: r})
			b = b[size:]
			continue
		}
		b = b[1:]
	}
	return keys
}

var csiKeys = map[string]string{
	"A": "up", "B": "down", "C": "right", "D": "left", "H": "home", "F": "end",
	"1~": "home", "4~": "end", "7~": "home", "8~": "end",
	"5~": "pgup", "6~": "pgdn", "3~": "delete", "Z": "backtab",
}

func decodeRune(b []byte) (rune, int) {
	r, size := utf8.DecodeRune(b)
	if size == 0 {
		size = 1
	}
	return r, size
}

func (a *tuiApp) handleKey(k tuiKey) {
	switch a.mode {
	case modeHelp:
		a.mode = modeList
		return
	case modeConfirm:
		if k.name == "rune" && (k.r == 'y' || k.r == 'Y') {
			yes := a.confirm.yes
			a.mode, a.confirm = modeList, nil
			yes()
			return
		}
		if k.name == "esc" || k.name == "ctrl-c" || k.name == "rune" && (k.r == 'n' || k.r == 'N' || k.r == 'q') {
			a.mode, a.confirm = modeList, nil
			a.setFlash('i', "cancelled")
		}
		return
	case modeForm:
		a.formKey(k)
		return
	case modeView:
		a.viewKey(k)
		return
	case modeFilter:
		switch k.name {
		case "esc", "ctrl-c":
			a.filter, a.mode = "", modeList
		case "enter":
			a.mode = modeList
		case "backspace":
			if r := []rune(a.filter); len(r) > 0 {
				a.filter = string(r[:len(r)-1])
			}
		case "ctrl-u":
			a.filter = ""
		case "up", "down":
			a.move(map[string]int{"up": -1, "down": 1}[k.name])
		case "rune":
			a.filter += string(k.r)
		}
		a.clampSel()
		return
	}

	vis := a.visible()
	var cur *treeNode
	if a.sel >= 0 && a.sel < len(vis) {
		n := vis[a.sel].node
		cur = &n
	}
	switch k.name {
	case "up":
		a.move(-1)
	case "down":
		a.move(1)
	case "pgup":
		a.move(-a.listHeight())
	case "pgdn":
		a.move(a.listHeight())
	case "home":
		a.move(-len(vis))
	case "end":
		a.move(len(vis))
	case "left":
		if cur != nil && cur.Child != "" && !a.collapsed[cur.Path] {
			a.collapsed[cur.Path] = true
		} else if cur != nil && cur.Depth > 0 {
			a.selectPath(cur.Path[:strings.LastIndex(cur.Path, "/")])
		}
	case "right":
		if cur != nil && cur.Child != "" {
			delete(a.collapsed, cur.Path)
		}
	case "enter":
		a.doAttach(cur)
	case "tab":
		a.view = (a.view + 1) % viewCount
		a.loadPreview()
	case "backtab":
		a.view = (a.view + viewCount - 1) % viewCount
		a.loadPreview()
	case "esc":
		if a.filter != "" {
			a.filter = ""
			a.clampSel()
		}
	case "ctrl-c":
		a.quit = true
	case "rune":
		switch k.r {
		case 'q':
			a.quit = true
		case 'j':
			a.move(1)
		case 'k':
			a.move(-1)
		case 'g':
			a.move(-len(vis))
		case 'G':
			a.move(len(vis))
		case 'a':
			a.doAttach(cur)
		case ' ':
			if cur != nil && cur.Child != "" {
				a.collapsed[cur.Path] = !a.collapsed[cur.Path]
			}
		case 'n':
			a.openNew(cur, false)
		case 'c':
			a.openNew(cur, true)
		case 'd':
			a.doDelete(cur)
		case 'x':
			a.doPruneDead()
		case 'm':
			a.openNote(cur)
		case 'i':
			a.openSend(cur)
		case 'o':
			a.openFull(cur)
		case ':':
			a.openCommand(cur)
		case 'r':
			a.refresh()
			a.setFlash('i', "refreshed")
		case '/':
			a.mode = modeFilter
		case '?':
			a.mode = modeHelp
		}
	}
}

func (a *tuiApp) formKey(k tuiKey) {
	f := a.form
	fld := &f.fields[f.focus]
	switch k.name {
	case "esc", "ctrl-c":
		a.mode, a.form = modeList, nil
		a.setFlash('i', "cancelled")
	case "tab", "down":
		f.focus = (f.focus + 1) % len(f.fields)
	case "backtab", "up":
		if k.name == "up" && len(f.fields) == 1 && f.recall != "" {
			fld.value = f.recall
			break
		}
		f.focus = (f.focus + len(f.fields) - 1) % len(f.fields)
	case "backspace":
		if r := []rune(fld.value); len(r) > 0 {
			fld.value = string(r[:len(r)-1])
		}
	case "ctrl-u":
		fld.value = ""
	case "enter":
		vals := make([]string, len(f.fields))
		for i := range f.fields {
			vals[i] = strings.TrimSpace(f.fields[i].value)
		}
		a.mode, a.form = modeList, nil
		if msg := f.submit(vals); msg != "" {
			f.err = msg
			a.mode, a.form = modeForm, f
		}
	case "rune":
		fld.value += string(k.r)
	}
}

// --- actions -----------------------------------------------------------

// exec runs one pairmux subcommand in-process against socket and returns its
// envelope.
func (a *tuiApp) exec(socket string, args ...string) output.Envelope {
	var buf bytes.Buffer
	sub := &Ctx{Mux: a.c.layer(socket), Choice: a.c.Choice, JSON: true, StateDir: a.c.StateDir, Stdout: &buf}
	sub.dispatch(args)
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

// --- model -------------------------------------------------------------

func (a *tuiApp) refresh() {
	nodes, err := a.c.walkTree(true)
	a.nodes, a.listErr = nodes, err
	a.clampSel()
	a.loadPreview()
}

type visRow struct {
	node   treeNode
	prefix string
}

// visible is the list as drawn: the tree minus collapsed subtrees, or with a
// filter, every matching terminal by its full path.
func (a *tuiApp) visible() []visRow {
	var out []visRow
	if a.filter != "" {
		q := strings.ToLower(a.filter)
		for _, n := range a.nodes {
			if strings.Contains(strings.ToLower(n.Path), q) || strings.Contains(strings.ToLower(n.Pending), q) ||
				strings.Contains(string(n.Status), q) {
				out = append(out, visRow{node: n})
			}
		}
		return out
	}
	lastAt := map[int]bool{}
	hideBelow := ""
	for _, n := range a.nodes {
		if hideBelow != "" && strings.HasPrefix(n.Path, hideBelow) {
			continue
		}
		hideBelow = ""
		lastAt[n.Depth] = n.Last
		var p strings.Builder
		for d := 1; d < n.Depth; d++ {
			if lastAt[d] {
				p.WriteString("   ")
			} else {
				p.WriteString("│  ")
			}
		}
		if n.Depth > 0 {
			if n.Last {
				p.WriteString("└─ ")
			} else {
				p.WriteString("├─ ")
			}
		}
		out = append(out, visRow{node: n, prefix: p.String()})
		if n.Child != "" && a.collapsed[n.Path] {
			hideBelow = n.Path + "/"
		}
	}
	return out
}

func (a *tuiApp) clampSel() {
	vis := a.visible()
	if a.selPath != "" {
		for i, r := range vis {
			if r.node.Path == a.selPath {
				a.sel = i
				return
			}
		}
	}
	if a.sel >= len(vis) {
		a.sel = len(vis) - 1
	}
	if a.sel < 0 {
		a.sel = 0
	}
	if len(vis) > 0 {
		a.selPath = vis[a.sel].node.Path
	}
}

func (a *tuiApp) move(d int) {
	vis := a.visible()
	if len(vis) == 0 {
		return
	}
	a.sel = max(0, min(len(vis)-1, a.sel+d))
	a.selPath = vis[a.sel].node.Path
	a.loadPreview()
}

func (a *tuiApp) selectPath(p string) {
	a.selPath = p
	a.clampSel()
	a.loadPreview()
}

// loadPreview captures the selected terminal: its screen when live, the tail
// of its journal when dead.
func (a *tuiApp) loadPreview() {
	a.preview, a.previewMeta = nil, ""
	vis := a.visible()
	if a.sel < 0 || a.sel >= len(vis) {
		return
	}
	n := vis[a.sel].node
	var meta []string
	if !n.Term.Meta.CreatedAt.IsZero() {
		meta = append(meta, "created "+humanizeAge(time.Since(n.Term.Meta.CreatedAt))+" ago")
	}
	if n.Term.Meta.Shell == "" && n.Term.Meta.Name != "" {
		meta = append(meta, "program")
	} else if n.Term.Meta.Shell != "" {
		meta = append(meta, n.Term.Meta.Shell)
	}
	if n.Child != "" {
		meta = append(meta, fmt.Sprintf("drives %d below", a.descendants(n)))
	}
	if n.Lock > 0 {
		meta = append(meta, fmt.Sprintf("lock pid %d", n.Lock))
	}
	a.previewMeta = strings.Join(meta, " · ")

	if a.view == viewActivity {
		a.preview = activityLines(n)
		return
	}
	var text string
	if n.Term.Alive && n.Term.PaneID != "" {
		if s, err := a.c.layer(n.Socket).CapturePane(n.Term.PaneID, 0); err == nil {
			text = s
		}
	}
	if text == "" {
		j := &journal.Journal{Dir: n.Term.Dir}
		if raw, _, err := j.TailBytes(32 << 10); err == nil {
			text = cleanNoTrunc(raw, "")
		}
	}
	text = trimTrailingBlank(strings.ReplaceAll(text, "\r", ""))
	if text != "" {
		a.preview = strings.Split(text, "\n")
	}
}

// --- drawing -----------------------------------------------------------

type seg struct {
	text  string
	style string // SGR parameters, "" for plain
}

type tline []seg

func (l tline) width() int {
	w := 0
	for _, s := range l {
		w += strWidth(s.text)
	}
	return w
}

func strWidth(s string) int {
	w := 0
	for _, r := range s {
		w += max(vt.RuneWidth(r), 0)
	}
	return w
}

// clip cuts s to at most w cells, ending in … when it had to cut.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if strWidth(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := max(vt.RuneWidth(r), 0)
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteString("…")
	return b.String()
}

func pad(s string, w int) string {
	s = clip(s, w)
	if n := w - strWidth(s); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}

// render writes l into exactly w cells, with fill as the style of padding.
func (a *tuiApp) render(l tline, w int, fill string) string {
	var b strings.Builder
	used := 0
	for _, s := range l {
		if used >= w {
			break
		}
		t := clip(s.text, w-used)
		used += strWidth(t)
		b.WriteString(a.sty(s.style, t))
	}
	if used < w {
		b.WriteString(a.sty(fill, strings.Repeat(" ", w-used)))
	}
	return b.String()
}

func (a *tuiApp) sty(style, s string) string {
	if a.noColor || style == "" || s == "" {
		return s
	}
	return "\x1b[" + style + "m" + s + "\x1b[0m"
}

const (
	stAccent  = "1;38;5;16;48;5;79"
	stTitle   = "1"
	stDim     = "38;5;245"
	stSel     = "1;48;5;237"
	stRunning = "38;5;78"
	stAwait   = "1;38;5;220"
	stIdle    = "38;5;111"
	stDead    = "38;5;203"
	stBorder  = "38;5;240"
	stKey     = "1;38;5;79"
	stOK      = "38;5;78"
	stErr     = "1;38;5;203"
	stTab     = "1;38;5;16;48;5;250"
)

func statusLook(s core.Status) (glyph, style, label string) {
	switch s {
	case core.StatusRunning:
		return "●", stRunning, "running"
	case core.StatusAwaitingInput:
		return "◆", stAwait, "needs input"
	case core.StatusIdle:
		return "○", stIdle, "idle"
	case core.StatusDead:
		return "✕", stDead, "dead"
	}
	return "?", stDim, string(s)
}

func (a *tuiApp) listHeight() int { return max(1, a.bodyHeight()-1) }

func (a *tuiApp) bodyHeight() int {
	h := a.h - 3
	if a.w < 110 {
		h = (a.h - 3) * 55 / 100
	}
	return max(3, h)
}

func (a *tuiApp) draw() {
	w, h := max(a.w, 40), max(a.h, 10)
	lines := make([]string, 0, h)

	// Header.
	counts := map[core.Status]int{}
	for _, n := range a.nodes {
		counts[n.Status]++
	}
	hdr := tline{{" pairmux ", stAccent}, {"  " + a.c.Mux.SocketName() + " · " + a.c.Mux.Backend(), stDim}, {"   ", ""}}
	for _, s := range []core.Status{core.StatusRunning, core.StatusAwaitingInput, core.StatusIdle, core.StatusDead} {
		if counts[s] > 0 {
			g, st, label := statusLook(s)
			hdr = append(hdr, seg{g + " ", st}, seg{fmt.Sprintf("%d %s   ", counts[s], label), ""})
		}
	}
	clock := time.Now().Format("15:04:05")
	if gap := w - hdr.width() - strWidth(clock) - 1; gap > 0 {
		hdr = append(hdr, seg{strings.Repeat(" ", gap), ""})
	}
	hdr = append(hdr, seg{clock + " ", stDim})
	lines = append(lines, a.render(hdr, w, ""))

	vis := a.visible()
	wide := w >= 110
	listW := w
	if wide {
		listW = w * 52 / 100
	}
	bodyH := h - 3
	listH := bodyH
	if !wide {
		listH = bodyH * 55 / 100
	}
	if a.viewer != nil && (a.mode == modeView || a.mode == modeForm && a.form != nil && a.form.title == commandTitle) {
		lines = append(lines, a.drawViewer(w, bodyH)...)
	} else if listLines := a.drawList(vis, listW, listH); wide {
		prevLines := a.drawPreview(w-listW-1, bodyH)
		for i := 0; i < bodyH; i++ {
			lines = append(lines, listLines[i]+a.sty(stBorder, "│")+prevLines[i])
		}
	} else {
		lines = append(lines, listLines...)
		lines = append(lines, a.drawPreview(w, bodyH-listH)...)
	}

	// Footer: flash or contextual hint, then keys.
	var foot tline
	if a.flash != "" && time.Since(a.flashAt) < 6*time.Second {
		st := stDim
		switch a.flashKind {
		case 'o':
			st = stOK
		case 'e':
			st = stErr
		}
		foot = tline{{" " + a.flash, st}}
	} else if a.listErr != nil {
		foot = tline{{" error listing terminals: " + a.listErr.Error(), stErr}}
	} else if a.mode == modeView {
		foot = tline{{" " + viewerHint(a.viewer), stDim}}
	} else if a.sel < len(vis) && len(vis) > 0 {
		foot = a.hintFor(vis[a.sel].node)
	}
	lines = append(lines, a.render(foot, w, ""))
	if a.mode == modeView {
		lines = append(lines, a.render(a.viewerKeyBar(), w, ""))
	} else {
		lines = append(lines, a.render(a.keyBar(), w, ""))
	}

	// Overlays.
	switch a.mode {
	case modeHelp:
		a.overlay(lines, w, "Keys", helpLines())
	case modeConfirm:
		body := wrap(a.confirm.msg, min(64, w-8))
		body = append(body, "", "y  yes        n / esc  no")
		a.overlay(lines, w, a.confirm.title, body)
	case modeForm:
		a.overlayForm(lines, w)
	}

	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range lines {
		if i >= h {
			break
		}
		b.WriteString(l)
		b.WriteString("\x1b[0m\x1b[K")
		if i < h-1 {
			b.WriteString("\r\n")
		}
	}
	io.WriteString(a.out, b.String())
}

func (a *tuiApp) drawList(vis []visRow, w, h int) []string {
	out := make([]string, 0, h)
	title := " Terminals"
	if a.filter != "" || a.mode == modeFilter {
		cursor := ""
		if a.mode == modeFilter {
			cursor = "█"
		}
		title = " Filter: " + a.filter + cursor
	}
	out = append(out, a.render(tline{{title, stTitle}, {fmt.Sprintf("  %d", len(vis)), stDim}}, w, ""))
	rows := h - 1
	if a.sel < a.offset {
		a.offset = a.sel
	}
	if a.sel >= a.offset+rows {
		a.offset = a.sel - rows + 1
	}
	a.offset = max(0, min(a.offset, max(0, len(vis)-rows)))

	if len(vis) == 0 {
		msg := "  no terminals yet — press n to create one"
		if a.filter != "" {
			msg = "  nothing matches the filter (esc clears it)"
		}
		out = append(out, a.render(tline{{msg, stDim}}, w, ""))
	}
	nameW := 12
	for _, r := range vis {
		shown := r.node.Term.Name
		if a.filter != "" {
			shown = r.node.Path
		}
		nameW = max(nameW, strWidth(r.prefix)+4+strWidth(shown))
	}
	nameW = min(nameW, max(16, w/2))
	for i := a.offset; i < len(vis) && len(out) < h; i++ {
		r := vis[i]
		n := r.node
		g, st, label := statusLook(n.Status)
		fold := " "
		if n.Child != "" {
			fold = "▾"
			if a.collapsed[n.Path] {
				fold = "▸"
			}
		}
		name := n.Term.Name
		if a.filter != "" {
			name = n.Path
		}
		selected := i == a.sel && len(vis) > 0
		mark := " "
		if selected {
			mark = "▌"
		}
		age := "-"
		if !n.LastMod.IsZero() {
			age = humanizeAge(time.Since(n.LastMod))
		}
		detail := n.Pending
		var badges []string
		if n.Notes > 0 {
			badges = append(badges, fmt.Sprintf("note:%d", n.Notes))
		}
		if n.Child != "" && a.collapsed[n.Path] {
			badges = append(badges, fmt.Sprintf("+%d", a.descendants(n)))
		}
		l := tline{
			{mark, stKey},
			{r.prefix, stBorder},
			{fold + " ", stDim},
			{g + " ", st},
			{pad(name, max(1, nameW-strWidth(r.prefix)-4)) + "  ", ""},
			{pad(label, 13), st},
			{pad(age, 6), stDim},
		}
		if len(badges) > 0 {
			l = append(l, seg{strings.Join(badges, " ") + " ", stAwait})
		}
		l = append(l, seg{detail, stDim})
		fill := ""
		if selected {
			for k := range l {
				if l[k].style == "" {
					l[k].style = stSel
				} else {
					l[k].style += ";48;5;237"
				}
			}
			fill = stSel
		}
		out = append(out, a.render(l, w, fill))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out
}

func (a *tuiApp) drawPreview(w, h int) []string {
	out := make([]string, 0, h)
	if h <= 0 {
		return out
	}
	vis := a.visible()
	var title tline
	for i, name := range viewNames {
		st := stDim
		if i == a.view {
			st = stTab
		}
		title = append(title, seg{" " + name + " ", st})
	}
	if a.sel < len(vis) && len(vis) > 0 {
		n := vis[a.sel].node
		_, st, label := statusLook(n.Status)
		title = append(title, seg{"  " + n.Path + "  ", ""}, seg{label, st})
	}
	out = append(out, a.render(title, w, ""))
	if a.previewMeta != "" && h > 2 {
		out = append(out, a.render(tline{{" " + a.previewMeta, stDim}}, w, ""))
	}
	room := h - len(out)
	src := a.preview
	if len(src) > room {
		src = src[len(src)-room:]
	}
	for _, l := range src {
		out = append(out, a.render(tline{{" " + strings.ReplaceAll(l, "\t", "    "), ""}}, w, ""))
	}
	if len(a.preview) == 0 && room > 0 && len(vis) > 0 {
		out = append(out, a.render(tline{{" (no output yet)", stDim}}, w, ""))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out
}

func (a *tuiApp) hintFor(n treeNode) tline {
	switch n.Status {
	case core.StatusAwaitingInput:
		return tline{{" ◆ " + n.Path + " is waiting for input — ⏎ attach to answer it, or i to send a line", stAwait}}
	case core.StatusDead:
		return tline{{" ✕ " + n.Path + " is dead — d deletes its journal, x deletes every dead terminal", stDead}}
	}
	if n.Notes > 0 {
		return tline{{fmt.Sprintf(" %s has %d note(s) its agent has not read yet", n.Path, n.Notes), stDim}}
	}
	if n.Child != "" {
		return tline{{" " + n.Path + " drives its own terminals — space folds them, c creates one under it", stDim}}
	}
	if a.c.Mux.Backend() == mux.BackendTmux && insideTmux() {
		return tline{{" ⏎ attaches as a nested tmux inside this one — detach with Ctrl-b Ctrl-b d", stDim}}
	}
	return nil
}

func (a *tuiApp) keyBar() tline {
	keys := [][2]string{{"⏎", "attach"}, {"n", "new"}, {"c", "new under"}, {"d", "kill/delete"}, {"i", "send"},
		{"m", "note"}, {"tab", "view"}, {"o", "open"}, {":", "command"}, {"?", "help"}, {"q", "quit"}}
	var l tline
	for _, k := range keys {
		l = append(l, seg{" " + k[0], stKey}, seg{" " + k[1] + " ", stDim})
	}
	return l
}

func helpLines() []string {
	return []string{
		"↑ ↓  j k        move            PgUp PgDn g G   jump",
		"⏎  a            attach (detach: Ctrl-b d)",
		"n               new terminal in the top layer",
		"c               new terminal under the selected one",
		"d               kill a live terminal / delete a dead one",
		"x               delete every dead terminal's journal",
		"m               leave a note for the terminal's agent",
		"i               send a line of input",
		"space ← →       fold / unfold a terminal's sub-terminals",
		"/               filter by path, command or status",
		"tab             preview: screen ⇄ activity (commands, exit codes, notes)",
		"o               open it full screen: whole output history / activity",
		":               run any pairmux command; {} = the selected path",
		"r               refresh now            q  Ctrl-C  quit",
		"",
		"Agents below may only drive their own subtree; the top is yours.",
	}
}

// overlay draws a centred box over lines.
func (a *tuiApp) overlay(lines []string, w int, title string, body []string) {
	inner := len(title) + 4
	for _, b := range body {
		inner = max(inner, strWidth(b)+2)
	}
	inner = min(inner, w-4)
	top := max(1, (len(lines)-len(body)-2)/2)
	left := max(0, (w-inner-2)/2)
	put := func(row int, l tline) {
		if row < 0 || row >= len(lines) {
			return
		}
		lines[row] = strings.Repeat(" ", left) + a.render(l, inner+2, "") + strings.Repeat(" ", max(0, w-left-inner-2))
	}
	t := " " + title + " "
	put(top, tline{{"┌─", stBorder}, {t, stTitle}, {strings.Repeat("─", max(0, inner-strWidth(t)-1)) + "┐", stBorder}})
	for i, b := range body {
		put(top+1+i, tline{{"│ ", stBorder}, {pad(b, inner-2), ""}, {" │", stBorder}})
	}
	put(top+1+len(body), tline{{"└" + strings.Repeat("─", inner) + "┘", stBorder}})
}

func (a *tuiApp) overlayForm(lines []string, w int) {
	f := a.form
	inner := min(72, w-6)
	var body []string
	for i, fld := range f.fields {
		cursor := ""
		marker := "  "
		if i == f.focus {
			cursor, marker = "█", "▸ "
		}
		v := fld.value + cursor
		if fw := inner - 14; strWidth(v) > fw {
			r := []rune(v)
			for strWidth(string(r)) > fw-1 {
				r = r[1:]
			}
			v = "…" + string(r)
		}
		body = append(body, marker+pad(fld.label, 10)+" "+v)
		if i == f.focus && fld.hint != "" {
			body = append(body, "             "+fld.hint)
		}
	}
	if f.err != "" {
		body = append(body, "")
		for _, l := range wrap("✕ "+f.err, inner-2) {
			body = append(body, l)
		}
	}
	body = append(body, "", "tab next field   ⏎ submit   esc cancel")
	if len(body) > 0 {
		body[0] = pad(body[0], inner-2) // the form keeps its width as values change
	}
	a.overlay(lines, w, f.title, body)
}

func wrap(s string, w int) []string {
	var out []string
	var cur string
	for _, word := range strings.Fields(s) {
		if cur != "" && strWidth(cur)+1+strWidth(word) > w {
			out = append(out, cur)
			cur = word
			continue
		}
		if cur != "" {
			cur += " "
		}
		cur += word
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
