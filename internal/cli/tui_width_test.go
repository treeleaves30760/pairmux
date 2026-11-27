package cli

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/vt"
)

// fakeTerm is a terminal: a screen grid with the semantics the TUI relies on
// — autowrap off, so after a write into the last column the cursor stays ON
// that column and the next character overwrites it, and EL (ESC[K) erases
// from the cursor cell inclusive — and a DSR (ESC[6n) answer with the cursor
// position, as a narrow terminal, as one that draws ambiguous glyphs two
// cells wide, or not at all ("silent"). Replies arrive asynchronously, split
// at awkward places and interleaved with what the human typed meanwhile: "j"
// before them, then a Shift-F3 (ESC[1;2R, the shape of a row-1 cursor report)
// and "k" in the middle, as on a real tty.
type fakeTerm struct {
	mode     string // "narrow", "wide" or "silent"
	in       chan []byte
	mu       sync.Mutex
	w, h     int
	row, col int // 1-based
	grid     [][]string
	n        int // bytes written
}

func newFakeTerm(mode string) *fakeTerm { return newFakeScreen(mode, 200, 50) }

func newFakeScreen(mode string, w, h int) *fakeTerm {
	f := &fakeTerm{mode: mode, in: make(chan []byte, 256), w: w, h: h, row: 1, col: 1}
	f.grid = make([][]string, h)
	for i := range f.grid {
		f.grid[i] = make([]string, w)
	}
	return f
}

// cells is the terminal's own idea of a rune's width.
func (f *fakeTerm) cells(r rune) int { return termCells(f.mode == "wide", r) }

func termCells(wide bool, r rune) int {
	if wide && (strings.ContainsRune(probeGlyphs, r) || vt.IsAmbiguous(r)) && vt.RuneWidth(r) == 1 {
		return 2
	}
	return max(vt.RuneWidth(r), 0)
}

var csiRE = regexp.MustCompile(`^\x1b\[([0-9;?]*)([A-Za-z])`)

func (f *fakeTerm) erase(row, from int) {
	for c := from; c <= f.w; c++ {
		f.grid[row-1][c-1] = ""
	}
}

// screen is the grid's rows; an unwritten cell reads as NUL.
func (f *fakeTerm) screen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, f.h)
	for i, r := range f.grid {
		var b strings.Builder
		for _, c := range r {
			if c == "" {
				c = "\x00"
			}
			b.WriteString(c)
		}
		out[i] = b.String()
	}
	return out
}

// lastCell is the glyph in the last column of row y (0-based).
func (f *fakeTerm) lastCell(y int) string { return f.grid[y][f.w-1] }

func (f *fakeTerm) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n += len(p)
	var replies []byte
	for b := p; len(b) > 0; {
		if m := csiRE.FindSubmatch(b); m != nil {
			params, final := string(m[1]), m[2][0]
			switch {
			case final == 'n' && params == "6":
				replies = append(replies, fmt.Sprintf("\x1b[%d;%dR", f.row, f.col)...)
			case final == 'H':
				f.row, f.col = 1, 1
				if params != "" {
					fmt.Sscanf(params, "%d;%d", &f.row, &f.col)
				}
			case final == 'K' && (params == "" || params == "0"):
				f.erase(f.row, f.col)
			case final == 'K' && params == "2":
				f.erase(f.row, 1)
			case final == 'J' && params == "2":
				for r := 1; r <= f.h; r++ {
					f.erase(r, 1)
				}
			}
			b = b[len(m[0]):]
			continue
		}
		switch b[0] {
		case '\r':
			f.col = 1
			b = b[1:]
			continue
		case '\n':
			f.row = min(f.row+1, f.h)
			b = b[1:]
			continue
		}
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		cw := f.cells(r)
		if cw == 0 || f.col+cw-1 > f.w {
			continue // a wide glyph that does not fit is not drawn
		}
		f.grid[f.row-1][f.col-1] = string(r)
		if cw == 2 {
			f.grid[f.row-1][f.col] = "\x01" // the right half of a wide glyph
		}
		f.col = min(f.col+cw, f.w) // autowrap off: the cursor stays on the last column
	}
	if !bytes.Contains(p, []byte("\x1b[6n")) {
		return len(p), nil
	}
	if f.mode == "silent" {
		f.in <- []byte("j")
		return len(p), nil
	}
	go func() {
		f.in <- []byte("j")
		half := len(replies)/2 + 3 // mid-reply
		f.in <- replies[:half]
		f.in <- []byte("\x1b[1;2R") // Shift-F3
		f.in <- []byte("k")
		for _, c := range bytes.SplitAfter(replies[half:], []byte("R")) {
			if len(c) > 0 {
				f.in <- c
			}
		}
	}()
	return len(p), nil
}

func withCells(t *testing.T, ct cellTable) {
	t.Helper()
	old := tuiCells
	tuiCells = ct
	t.Cleanup(func() { tuiCells = old })
}

func TestProbeWidths(t *testing.T) {
	t.Setenv(AmbiguousWidthEnv, "")
	for _, mode := range []string{"narrow", "wide"} {
		f := newFakeTerm(mode)
		ct, rest := detectCells(f, f.in)
		if string(rest) != "j\x1b[1;2Rk" {
			t.Errorf("%s: keys typed during the probe = %q, want j, Shift-F3, k", mode, rest)
		}
		want := 1
		if mode == "wide" {
			want = 2
		}
		for _, r := range probeGlyphs {
			if got := 1 + boolInt(ct.wide[r]); got != want {
				t.Errorf("%s: %q measured %d cells, want %d", mode, r, got, want)
			}
		}
		if ct.ambWide != (mode == "wide") {
			t.Errorf("%s: ambWide = %v", mode, ct.ambWide)
		}
		if mode == "narrow" && (ct.wide != nil || ct.ambWide) {
			t.Errorf("narrow terminal changed the table: %+v", ct)
		}
	}
}

// A terminal that does not answer costs one short timeout, not one per glyph,
// keeps today's widths, and loses no keystroke.
func TestProbeWithoutAnswerFallsBack(t *testing.T) {
	t.Setenv(AmbiguousWidthEnv, "")
	f := newFakeTerm("silent")
	start := time.Now()
	ct, rest := detectCells(f, f.in)
	if d := time.Since(start); d > 2*probeTimeout+100*time.Millisecond {
		t.Errorf("probe took %v", d)
	}
	if ct.wide != nil || ct.ambWide {
		t.Errorf("no answer changed the table: %+v", ct)
	}
	if string(rest) != "j" {
		t.Errorf("rest = %q", rest)
	}
}

func TestProbeRejectsGarbledReplies(t *testing.T) {
	in := make(chan []byte, 4)
	in <- []byte("\x1b[2;2R\x1b[2;9R")
	if _, _, ok := probeWidths(&bytes.Buffer{}, in, "●─", 100*time.Millisecond); ok {
		t.Fatal("a 8-cell glyph was accepted")
	}
	in <- []byte("\x1b[2;2R\x1b[5;3R")
	if _, rest, ok := probeWidths(&bytes.Buffer{}, in, "●─", 100*time.Millisecond); ok || string(rest) != "\x1b[5;3R" {
		t.Fatalf("a report from another row was taken as a reply: ok %v rest %q", ok, rest)
	}
	// A row-1 report has the shape of a modified F3 key (Shift-F3 is ESC[1;2R):
	// it is a key, never a reply, because the probe row is 2.
	in <- []byte("x\x1b[A\x1b[1;2R\x1b[2;3R\x1b[2;2Ry")
	w, rest, ok := probeWidths(&bytes.Buffer{}, in, "●─", 100*time.Millisecond)
	if !ok || w['●'] != 2 || w['─'] != 1 || string(rest) != "x\x1b[A\x1b[1;2Ry" {
		t.Fatalf("widths %v rest %q ok %v", w, rest, ok)
	}
}

func TestAmbiguousWidthEnvSkipsTheProbe(t *testing.T) {
	for _, tc := range []struct {
		env  string
		wide bool
	}{{"1", false}, {"2", true}} {
		t.Setenv(AmbiguousWidthEnv, tc.env)
		f := newFakeTerm("wide")
		ct, _ := detectCells(f, f.in)
		if f.n != 0 {
			t.Errorf("env %s: the probe still ran", tc.env)
		}
		if ct.ambWide != tc.wide || ct.wide['●'] != tc.wide {
			t.Errorf("env %s: table %+v", tc.env, ct)
		}
	}
}

// widthApp is a fleet that exercises every glyph of the frame: a tree with a
// folded layer, notes, a CJK command and preview content with CJK and
// ambiguous characters.
func widthApp(t *testing.T, w, h int) (*tuiApp, *bytes.Buffer) {
	var out bytes.Buffer
	dir := t.TempDir()
	a := &tuiApp{c: newTestCtx(nil, true), collapsed: map[string]bool{}, noColor: true, w: w, h: h, out: &out,
		results: make(chan tuiResult, 1)}
	a.nodes = []treeNode{
		node("boss", 0, false, true),
		node("boss/a", 1, false, true),
		node("boss/a/x", 2, true, false),
		node("boss/beta", 1, true, false),
		node("solo", 0, false, true),
		node("solo/z", 1, true, false),
		node("zed", 0, true, false),
	}
	statuses := []core.Status{core.StatusRunning, core.StatusAwaitingInput, core.StatusIdle, core.StatusDead, core.StatusIdle, core.StatusIdle, core.StatusRunning}
	for i := range a.nodes {
		a.nodes[i].Status = statuses[i]
		a.nodes[i].Term.Dir = dir
	}
	a.nodes[0].Pending = "make test 測試 — ok"
	a.nodes[1].Notes = 2
	a.collapsed["solo"] = true
	a.preview = []string{"╭─ 中文字 ─╮ ● … · ¹²", "plain line", strings.Repeat("─", 300)}
	a.previewMeta = "created 2s ago · bash"
	return a, &out
}

// frameLines returns the rows a draw wrote, without the cursor and erase
// sequences (colour is off).
func frameLines(out *bytes.Buffer) []string {
	s := strings.TrimPrefix(out.String(), "\x1b[H")
	out.Reset()
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\x1b[2K", ""), "\x1b[0m", "")
	return strings.Split(s, "\r\n")
}

func lineCells(wide bool, s string) int {
	n := 0
	for _, r := range s {
		n += termCells(wide, r)
	}
	return n
}

// Every drawn row fills the terminal exactly — measured as the terminal
// draws it — on a narrow and on an ambiguous-wide terminal, in both layouts
// and under every overlay.
func TestFrameFitsTheTerminalInBothModes(t *testing.T) {
	t.Setenv(AmbiguousWidthEnv, "")
	for _, mode := range []string{"narrow", "wide"} {
		f := newFakeTerm(mode)
		ct, _ := detectCells(f, f.in)
		withCells(t, ct)
		for _, w := range []int{60, 80, 120, 151} {
			a, out := widthApp(t, w, 30)
			a.selPath = "boss"
			a.clampSel()
			states := map[string]func(){
				"list": func() {},
				"help": func() { a.mode = modeHelp },
				"confirm": func() {
					a.mode, a.confirm = modeConfirm, &tuiConfirm{title: "kill boss?", msg: "this ends boss — and everything under it", yes: func() {}}
				},
				"form": func() {
					a.mode, a.confirm = modeList, nil
					a.openNew(nil, false)
					a.form.fields[0].value = strings.Repeat("長", 80)
				},
				"viewer": func() {
					a.mode, a.form = modeList, nil
					a.openViewer(&tuiViewer{title: "Output · boss", load: func() ([]string, string) {
						return append([]string{"中文 ─ │ ● …"}, a.preview...), "exit 0"
					}}, false)
				},
				"filter": func() {
					a.viewer, a.mode = nil, modeFilter
					a.filter = "b"
				},
			}
			for _, name := range []string{"list", "help", "confirm", "form", "viewer", "filter"} {
				states[name]()
				a.draw()
				for i, l := range frameLines(out) {
					if got := lineCells(mode == "wide", l); got != w {
						t.Errorf("%s terminal, %d cols, %s: row %d is %d cells:\n%q", mode, w, name, i, got, l)
					}
				}
			}
		}
	}
}

// colOf is the screen column where sub starts in a drawn row, counted as the
// terminal draws it.
func colOf(wide bool, line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return lineCells(wide, line[:i])
}

// On an ambiguous-wide terminal a click lands on what is drawn under it: a
// row, its fold arrow, and a border button.
func TestWideTerminalClicksHitWhatIsDrawn(t *testing.T) {
	t.Setenv(AmbiguousWidthEnv, "")
	f := newFakeTerm("wide")
	ct, _ := detectCells(f, f.in)
	withCells(t, ct)
	for _, w := range []int{120, 80} { // side by side, and stacked
		a, out := widthApp(t, w, 30)
		a.draw()
		lines := frameLines(out)
		find := func(sub string) (x, y int) {
			for y, l := range lines {
				if x := colOf(true, l, sub); x >= 0 {
					return x, y
				}
			}
			t.Fatalf("%q not drawn:\n%s", sub, strings.Join(lines, "\n"))
			return 0, 0
		}
		// A row: click the name; the selection follows.
		x, y := find("beta")
		a.mouse(tuiKey{name: "mouse", mx: x + 2, my: y})
		if a.selPath != "boss/beta" {
			t.Fatalf("%d cols: click on beta selected %q", w, a.selPath)
		}
		// A fold arrow: the stand-in for ▾ on boss's row.
		a.draw()
		lines = frameLines(out)
		x, y = find("v * boss") // fold arrow, status (running), name
		a.mouse(tuiKey{name: "mouse", mx: x, my: y})
		if !a.collapsed["boss"] {
			t.Fatalf("%d cols: click on boss's fold arrow did not fold it", w)
		}
		// A border button, after border glyphs that would be wide.
		a.draw()
		lines = frameLines(out)
		x, y = find("[new]")
		a.mouse(tuiKey{name: "mouse", mx: x + 2, my: y})
		if a.mode != modeForm {
			t.Fatalf("%d cols: click on new left mode %v", w, a.mode)
		}
		a.mode, a.form = modeList, nil
		a.draw()
		lines = frameLines(out)
		x, y = find("help")
		a.mouse(tuiKey{name: "mouse", mx: x + 1, my: y})
		if a.mode != modeHelp {
			t.Fatalf("%d cols: click on help left mode %v", w, a.mode)
		}
	}
}

// On a terminal that measured narrow the frame is byte-for-byte the one drawn
// without any probe.
func TestNarrowTerminalFrameIsUnchanged(t *testing.T) {
	t.Setenv(AmbiguousWidthEnv, "")
	draw := func() string {
		a, out := widthApp(t, 120, 30)
		a.flash, a.flashAt = "x", time.Time{}
		a.draw()
		s := out.String()
		return regexp.MustCompile(`\d\d:\d\d:\d\d`).ReplaceAllString(s, "")
	}
	withCells(t, cellTable{})
	before := draw()
	f := newFakeTerm("narrow")
	ct, _ := detectCells(f, f.in)
	withCells(t, ct)
	if after := draw(); after != before {
		t.Fatalf("narrow probe changed the frame:\n%q\n%q", before, after)
	}
	if !strings.Contains(before, "╭") || !strings.Contains(before, "●") {
		t.Fatal("frame lost its glyphs")
	}
}

// Every boxed row ends with its box's right edge in the terminal's last
// column, and every cell of the frame is drawn. With autowrap off the cursor
// stays on the last column after a full-width row, so an erase-to-end-of-line
// written after the row would wipe that cell: the right border v1.3.0 lost.
func TestFrameKeepsTheRightBorderColumn(t *testing.T) {
	t.Setenv(AmbiguousWidthEnv, "")
	edges := map[string]bool{"╮": true, "│": true, "╯": true, "+": true, "|": true}
	for _, mode := range []string{"narrow", "wide"} {
		f := newFakeTerm(mode)
		ct, _ := detectCells(f, f.in)
		withCells(t, ct)
		for _, w := range []int{60, 90, 120, 151} {
			const h = 20
			a, _ := widthApp(t, w, h)
			scr := newFakeScreen(mode, w, h)
			a.out = scr
			a.draw()
			a.draw() // a redraw over a full screen, as every tick does
			rows := scr.screen()
			for y := 1; y < h-1; y++ {
				if !edges[scr.lastCell(y)] {
					t.Errorf("%s terminal, %d cols: row %d ends with %q, not a box edge:\n%q", mode, w, y, scr.lastCell(y), rows[y])
				}
			}
			for y, r := range rows {
				if strings.Contains(r, "\x00") {
					t.Errorf("%s terminal, %d cols: row %d has undrawn cells:\n%q", mode, w, y, r)
				}
			}
		}
	}
}
