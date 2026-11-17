package cli

import (
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

	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/muxd"
	"github.com/treeleaves30760/pairmux/internal/output"
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

	w, h int
	quit bool

	// Mouse targets, rebuilt by every draw.
	hits          []hit
	listRect      rect
	lastClick     time.Time
	pendingAttach *treeNode

	restoreTTY func()
	reader     *ttyReader
	out        io.Writer
}

// --- lifecycle -------------------------------------------------------------

const (
	// Alternate screen, hidden cursor, SGR mouse reports (1000: clicks and
	// wheel, 1006: coordinates past column 223), and autowrap off (7l) so a
	// frame wider than a tiny terminal is cut instead of wrapping into the
	// rows below.
	tuiEnter = "\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h\x1b[?7l\x1b[H\x1b[2J"
	tuiLeave = "\x1b[?7h\x1b[?1006l\x1b[?1000l\x1b[0m\x1b[?25h\x1b[?1049l"
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

func (a *tuiApp) handleKey(k tuiKey) {
	if k.name == "mouse" {
		a.mouse(k)
		return
	}
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
		a.refilter()
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
			a.refilter()
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
		case '1', '2':
			a.view = int(k.r - '1')
			a.loadPreview()
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
		a.mode, a.form = a.formReturnMode(), nil
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
		a.mode, a.form = a.formReturnMode(), nil
		if msg := f.submit(vals); msg != "" {
			f.err = msg
			a.mode, a.form = modeForm, f
		}
	case "rune":
		fld.value += string(k.r)
	}
}

// formReturnMode is where closing a form lands: back in the reader the command
// line was opened from (: in a reader), else the tree. Landing in the tree with
// the reader still set would leave it drawn behind the next command form and
// swallow its pending result.
func (a *tuiApp) formReturnMode() tuiMode {
	if a.viewer != nil {
		return modeView
	}
	return modeList
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

// refilter reselects after the filter changed. The selection can land on
// another terminal, or on nothing, so the preview is recaptured: otherwise it
// would show the old terminal's screen under the new one's path until the next
// refresh tick.
func (a *tuiApp) refilter() {
	a.clampSel()
	a.loadPreview()
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
