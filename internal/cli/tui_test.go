package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/state"
)

func TestParseKeys(t *testing.T) {
	got := parseKeys([]byte("j\x1b[A\x1b[B\r\x7f\x1b\x1b[5~\x03é\x1b[Z\t"))
	var names []string
	for _, k := range got {
		if k.name == "rune" {
			names = append(names, string(k.r))
		} else {
			names = append(names, k.name)
		}
	}
	want := []string{"j", "up", "down", "enter", "backspace", "esc", "pgup", "ctrl-c", "é", "backtab", "tab"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("keys = %v\nwant   %v", names, want)
	}
	if k := parseKeys([]byte{0x1b}); len(k) != 1 || k[0].name != "esc" {
		t.Fatalf("lone ESC = %v", k)
	}
}

func node(path string, depth int, last bool, child bool) treeNode {
	n := treeNode{Path: path, Depth: depth, Last: last, Status: core.StatusIdle}
	n.Term = state.Terminal{Name: path[strings.LastIndex(path, "/")+1:], Alive: true}
	if child {
		n.Child = "sock-" + path
	}
	return n
}

func TestVisibleDrawsTheTreeAndFolds(t *testing.T) {
	a := &tuiApp{collapsed: map[string]bool{}, nodes: []treeNode{
		node("boss", 0, false, true),
		node("boss/a", 1, false, true),
		node("boss/a/x", 2, true, false),
		node("boss/b", 1, true, false),
		node("solo", 0, true, false),
	}}
	var got []string
	for _, r := range a.visible() {
		got = append(got, r.prefix+r.node.Term.Name)
	}
	want := []string{"boss", "├─ a", "│  └─ x", "└─ b", "solo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tree = %q\nwant   %q", got, want)
	}
	a.collapsed["boss/a"] = true
	if n := len(a.visible()); n != 4 {
		t.Fatalf("folded boss/a: %d rows, want 4", n)
	}
	a.collapsed["boss"] = true
	if n := len(a.visible()); n != 2 {
		t.Fatalf("folded boss: %d rows, want 2", n)
	}
	a.filter = "x"
	if v := a.visible(); len(v) != 1 || v[0].node.Path != "boss/a/x" {
		t.Fatalf("filter x = %+v", v)
	}
}

func TestClipAndPadCountCells(t *testing.T) {
	if got := clip("hello world", 6); got != "hello…" {
		t.Fatalf("clip = %q", got)
	}
	if got := clip("中文字", 5); strWidth(got) > 5 || !strings.HasSuffix(got, "…") {
		t.Fatalf("wide clip = %q (%d cells)", got, strWidth(got))
	}
	if got := pad("ab", 4); got != "ab  " {
		t.Fatalf("pad = %q", got)
	}
}

func TestLayerSocket(t *testing.T) {
	a := &tuiApp{c: newTestCtx(nil, true)}
	root := a.c.Mux.SocketName()
	if s, _ := a.layerSocket(""); s != root {
		t.Fatalf("top = %q", s)
	}
	if s, _ := a.layerSocket("boss/w"); s != state.ChildSocket(state.ChildSocket(root, "boss"), "w") {
		t.Fatalf("boss/w = %q", s)
	}
	if _, err := a.layerSocket("../x"); err == nil {
		t.Fatal("../x accepted")
	}
}

func TestParseKeysMouse(t *testing.T) {
	got := parseKeys([]byte("\x1b[<0;12;5M\x1b[<0;12;5m\x1b[<65;3;4M\x1b[<64;3;4M\x1b[<2;1;1Mj"))
	if len(got) != 4 {
		t.Fatalf("keys = %+v", got)
	}
	if k := got[0]; k.name != "mouse" || k.mx != 11 || k.my != 4 || k.wheel != 0 {
		t.Fatalf("click = %+v", k)
	}
	if got[1].wheel != 1 || got[2].wheel != -1 {
		t.Fatalf("wheel = %+v %+v", got[1], got[2])
	}
	if got[3].name != "rune" || got[3].r != 'j' {
		t.Fatalf("key after mouse = %+v", got[3])
	}
}

// Every button drawn into a border is clickable exactly where it is drawn,
// and a click does what its key does.
func TestBorderButtonsAreClickable(t *testing.T) {
	a := &tuiApp{collapsed: map[string]bool{}, noColor: true}
	line := a.border(7, 10, 40, false, "", []bItem{a.btn("?", "help")}, []bItem{a.btn("q", "quit")})
	if strWidth(line) != 40 {
		t.Fatalf("border is %d cells, want 40: %q", strWidth(line), line)
	}
	if len(a.hits) != 2 {
		t.Fatalf("hits = %+v", a.hits)
	}
	h := a.hits[0]
	if got := string([]rune(line)[h.x0-10 : h.x1-10]); got != "┘? help└" {
		t.Fatalf("first button covers %q", got)
	}
	a.mouse(tuiKey{name: "mouse", mx: h.x0 + 1, my: 7})
	if a.mode != modeHelp {
		t.Fatalf("clicking ? help left mode %v", a.mode)
	}
	a.mouse(tuiKey{name: "mouse", mx: 0, my: 0})
	if a.mode != modeList {
		t.Fatal("a click did not close help")
	}
	q := a.hits[1]
	a.mouse(tuiKey{name: "mouse", mx: q.x1 - 1, my: 7})
	if !a.quit {
		t.Fatal("clicking quit did not quit")
	}
}

// Items that do not fit are dropped rather than overflowing the box.
func TestBorderDropsWhatDoesNotFit(t *testing.T) {
	a := &tuiApp{noColor: true}
	line := a.border(0, 0, 16, true, "", []bItem{a.btn("n", "new"), a.btn("c", "child"), a.btn("d", "delete")}, []bItem{a.btn("o", "open")})
	if strWidth(line) != 16 || !strings.Contains(line, "new") || strings.Contains(line, "open") {
		t.Fatalf("border = %q", line)
	}
}

// A panic in a command run from : happens on a background goroutine, past
// run's deferred restore; it must become the reader's content, not a crash
// that leaves the terminal raw on the alternate screen.
func TestBackgroundCommandPanicBecomesTheReadersContent(t *testing.T) {
	a := &tuiApp{collapsed: map[string]bool{}, results: make(chan tuiResult, 1)}
	a.openViewer(&tuiViewer{title: "$ boom", load: func() ([]string, string) { panic("boom") }}, true)
	select {
	case r := <-a.results:
		if r.status != "exit 1" || !strings.Contains(strings.Join(r.lines, "\n"), "boom") {
			t.Fatalf("result = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
}

// Cancelling the command line opened from a reader goes back to that reader,
// rather than to the tree with the reader still set underneath.
func TestCommandFormFromAReaderReturnsToIt(t *testing.T) {
	a := &tuiApp{c: newTestCtx(nil, true), collapsed: map[string]bool{}, results: make(chan tuiResult, 1)}
	a.openViewer(&tuiViewer{title: "r", load: func() ([]string, string) { return []string{"x"}, "" }}, false)
	for _, k := range []tuiKey{{name: "rune", r: ':'}, {name: "esc"}} {
		a.handleKey(k)
	}
	if a.mode != modeView || a.viewer == nil {
		t.Fatalf("esc in the command form: mode=%v viewer=%v", a.mode, a.viewer)
	}
	for _, k := range []tuiKey{{name: "rune", r: ':'}, {name: "enter"}} { // empty command
		a.handleKey(k)
	}
	if a.mode != modeView {
		t.Fatalf("empty command: mode=%v", a.mode)
	}
	a.handleKey(tuiKey{name: "esc"})
	if a.mode != modeList || a.viewer != nil {
		t.Fatalf("esc in the reader: mode=%v viewer=%v", a.mode, a.viewer)
	}
}

// Typing a filter that moves the selection recaptures the preview, so it never
// shows one terminal's screen under another's path.
func TestFilterRecapturesThePreview(t *testing.T) {
	a := &tuiApp{collapsed: map[string]bool{}, nodes: []treeNode{
		node("api", 0, false, false), node("db", 0, true, false),
	}}
	for i := range a.nodes {
		a.nodes[i].Term.Alive, a.nodes[i].Status, a.nodes[i].Term.Dir = false, core.StatusDead, t.TempDir()
	}
	a.selPath, a.preview = "api", []string{"api's screen"}
	a.mode = modeFilter
	a.handleKey(tuiKey{name: "rune", r: 'b'})
	if a.selPath != "db" {
		t.Fatalf("selection = %q, want db", a.selPath)
	}
	if strings.Contains(strings.Join(a.preview, "\n"), "api's screen") {
		t.Fatalf("preview still shows api under db: %q", a.preview)
	}
}

// On a terminal smaller than the layout's minimum the frame is clipped to the
// terminal's rows: one more line would scroll the screen and shift every click.
func TestDrawNeverWritesPastTheLastRow(t *testing.T) {
	var out bytes.Buffer
	a := &tuiApp{c: newTestCtx(nil, true), collapsed: map[string]bool{}, noColor: true, w: 80, h: 6, out: &out}
	a.draw()
	if n := strings.Count(out.String(), "\n"); n != a.h-1 {
		t.Fatalf("frame has %d line breaks on a %d-row terminal", n, a.h)
	}
	if !strings.Contains(tuiEnter, "\x1b[?7l") || !strings.Contains(tuiLeave, "\x1b[?7h") {
		t.Fatal("autowrap is not off while the TUI owns the screen, or not restored")
	}
}
