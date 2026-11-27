package cli

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/detect"
	"github.com/treeleaves30760/pairmux/internal/mux"
)

// fitMux is a multiplexer whose panes have a size and screen modes, and that
// records every fit and unfit.
type fitMux struct {
	fakeBuiltin
	screens     map[string]*mux.ScreenInfo
	base        map[string][2]int
	calls       []string
	unsupported bool
}

func newFitMux() *fitMux {
	return &fitMux{screens: map[string]*mux.ScreenInfo{}, base: map[string][2]int{}}
}

func (f *fitMux) SocketName() string { return "pmx-fit" }

func (f *fitMux) pane(id string, s mux.ScreenInfo) {
	s.Cols, s.Rows = 220, 50
	f.screens[id] = &s
}

func (f *fitMux) PaneScreen(id string) (mux.ScreenInfo, error) {
	if f.unsupported {
		return mux.ScreenInfo{}, mux.ErrFitUnsupported
	}
	s, ok := f.screens[id]
	if !ok {
		return mux.ScreenInfo{}, fmt.Errorf("can't find pane: %s", id)
	}
	return *s, nil
}

func (f *fitMux) FitPane(id string, cols, rows int) error {
	f.calls = append(f.calls, fmt.Sprintf("fit %s %dx%d", id, cols, rows))
	s := f.screens[id]
	if s.Attached > 0 {
		return mux.ErrPaneWatched
	}
	if !s.Fitted {
		f.base[id] = [2]int{s.Cols, s.Rows}
	}
	s.Cols, s.Rows, s.Fitted = cols, rows, true
	return nil
}

func (f *fitMux) UnfitPane(id string) error {
	f.calls = append(f.calls, "unfit "+id)
	s := f.screens[id]
	if s == nil || !s.Fitted {
		return nil
	}
	s.Fitted = false
	if s.Attached == 0 {
		s.Cols, s.Rows = f.base[id][0], f.base[id][1]
	}
	return nil
}

func (f *fitMux) take() []string {
	c := f.calls
	f.calls = nil
	return c
}

// fitApp is a TUI on a 160x40 terminal over: a program terminal running a
// full-screen program (prog), a shell at its prompt (sh), a shell running an
// inline TUI (agent) and one running a build (build).
func fitApp(t *testing.T, f *fitMux) *tuiApp {
	t.Helper()
	var out bytes.Buffer
	a := &tuiApp{c: newTestCtx(nil, true), collapsed: map[string]bool{}, noColor: true, w: 160, h: 40, out: &out,
		results: make(chan tuiResult, 1), layerFn: func(string) mux.Mux { return f }}
	mk := func(name, pane, shell, pending string) treeNode {
		n := node(name, 0, false, false)
		n.Socket, n.Term.PaneID, n.Term.Dir = "pmx-fit", pane, t.TempDir()
		n.Term.Meta = core.Meta{Name: name, Shell: shell}
		n.Pending = pending
		n.Status = core.StatusRunning
		return n
	}
	a.nodes = []treeNode{
		mk("prog", "%1", "", ""),
		mk("sh", "%2", "/bin/zsh", ""),
		mk("agent", "%3", "/bin/zsh", "claude"),
		mk("build", "%4", "/bin/zsh", "make"),
	}
	f.pane("%1", mux.ScreenInfo{AltScreen: true, CursorHidden: true})
	f.pane("%2", mux.ScreenInfo{})
	f.pane("%3", mux.ScreenInfo{CursorHidden: true})
	f.pane("%4", mux.ScreenInfo{CursorHidden: true}) // a spinner
	raw := map[string]bool{"%2": true, "%3": true}   // zsh's line editor; the agent's TUI
	old := ttyStateOf
	ttyStateOf = func(n treeNode) detect.TTYState {
		return detect.TTYState{Known: true, Echo: !raw[n.Term.PaneID], Canonical: !raw[n.Term.PaneID]}
	}
	t.Cleanup(func() { ttyStateOf = old })
	a.selPath = "prog"
	a.clampSel()
	return a
}

// rest lets the selection rest past the settle delay.
func (a *tuiApp) rest(now *time.Time) {
	a.noteFitTarget(*now)
	*now = now.Add(fitSettle)
	a.fitTimers(*now)
}

func TestFitFollowsTheRestedSelection(t *testing.T) {
	f := newFitMux()
	a := fitApp(t, f)
	now := time.Unix(1000, 0)
	cols, rows := a.previewContent()
	if cols != 160-listWidth(160)-3 || rows != 40-2-2-2 {
		t.Fatalf("preview content %dx%d", cols, rows)
	}

	a.noteFitTarget(now)
	a.fitTimers(now.Add(fitSettle / 2))
	if c := f.take(); len(c) != 0 {
		t.Fatalf("fitted before the selection rested: %v", c)
	}
	now = now.Add(fitSettle)
	a.fitTimers(now)
	want := fmt.Sprintf("fit %%1 %dx%d", cols, rows)
	if c := f.take(); !reflect.DeepEqual(c, []string{want}) {
		t.Fatalf("rested on prog: %v, want %v", c, want)
	}
	if len(a.fit.recapture) != len(fitRedraws) {
		t.Fatalf("no recapture scheduled after a resize: %v", a.fit.recapture)
	}

	// Holding j through the list: nothing passed over is touched.
	for i := 0; i < 3; i++ {
		a.move(1)
		a.noteFitTarget(now)
		now = now.Add(fitSettle / 3)
		a.fitTimers(now)
	}
	if c := f.take(); len(c) != 0 {
		t.Fatalf("resized while the selection moved: %v", c)
	}
	// Resting on build (a spinner in a line-mode tty): prog gets its size
	// back, build is not resized.
	now = now.Add(fitSettle)
	a.fitTimers(now)
	if c := f.take(); !reflect.DeepEqual(c, []string{"unfit %1"}) {
		t.Fatalf("rested on build: %v", c)
	}
	if s := f.screens["%1"]; s.Cols != 220 || s.Rows != 50 || s.Fitted {
		t.Fatalf("prog after moving away: %+v", s)
	}

	// The shell at its prompt is never resized; the inline agent is.
	a.move(-2)
	a.rest(&now)
	if c := f.take(); len(c) != 0 {
		t.Fatalf("rested on sh: %v", c)
	}
	a.move(1)
	a.rest(&now)
	if c := f.take(); !reflect.DeepEqual(c, []string{fmt.Sprintf("fit %%3 %dx%d", cols, rows)}) {
		t.Fatalf("rested on agent: %v", c)
	}

	// A resized TUI fits again, to the new box.
	a.w, a.h = 120, 30
	a.rest(&now)
	c2, r2 := a.previewContent()
	if c := f.take(); !reflect.DeepEqual(c, []string{fmt.Sprintf("fit %%3 %dx%d", c2, r2)}) {
		t.Fatalf("after a resize: %v", c)
	}

	// Quitting gives every pane back its size.
	a.unfitAll()
	if c := f.take(); !reflect.DeepEqual(c, []string{"unfit %3"}) {
		t.Fatalf("quit: %v", c)
	}
	for id, s := range f.screens {
		if s.Fitted || s.Cols != 220 || s.Rows != 50 {
			t.Errorf("pane %s left at %+v", id, s)
		}
	}
}

func TestFitNeverTouchesAWatchedOrHiddenPane(t *testing.T) {
	f := newFitMux()
	a := fitApp(t, f)
	now := time.Unix(1000, 0)

	f.screens["%1"].Attached = 1
	a.rest(&now)
	if c := f.take(); len(c) != 0 {
		t.Fatalf("an attached pane was resized: %v", c)
	}
	// Fitted, then a human attaches elsewhere: the next re-evaluation lets go
	// (the multiplexer keeps the attached human's size).
	f.screens["%1"].Attached = 0
	a.applyFit(now)
	f.take()
	f.screens["%1"].Attached = 1
	a.applyFit(now)
	if c := f.take(); !reflect.DeepEqual(c, []string{"unfit %1"}) {
		t.Fatalf("after an attach: %v", c)
	}
	f.screens["%1"].Attached = 0

	// The activity tab and the full-screen reader do not show the screen.
	a.applyFit(now)
	f.take()
	a.view = viewActivity
	a.rest(&now)
	if c := f.take(); !reflect.DeepEqual(c, []string{"unfit %1"}) {
		t.Fatalf("activity tab: %v", c)
	}
	a.view = viewScreen
	a.rest(&now)
	f.take()

	// Too small a preview is not worth a resize.
	a.w, a.h = 120, 15 // 9 rows left for the screen
	a.rest(&now)
	if c := f.take(); !reflect.DeepEqual(c, []string{"unfit %1"}) {
		t.Fatalf("tiny terminal: %v", c)
	}

	// A dead terminal is never resized.
	a.w, a.h = 160, 40
	a.nodes[0].Term.Alive = false
	a.rest(&now)
	if c := f.take(); len(c) != 0 {
		t.Fatalf("dead terminal: %v", c)
	}
}

// An endpoint that cannot fit (an older built-in server) is asked once.
func TestFitUnsupportedIsQuiet(t *testing.T) {
	f := newFitMux()
	f.unsupported = true
	a := fitApp(t, f)
	now := time.Unix(1000, 0)
	a.rest(&now)
	if !a.fit.unsupported["pmx-fit"] || len(a.fit.fitted) != 0 {
		t.Fatalf("unsupported = %v fitted = %v", a.fit.unsupported, a.fit.fitted)
	}
	a.flash = ""
	a.applyFit(now)
	if len(f.take()) != 0 || a.flash != "" {
		t.Fatal("asked again, or told the human")
	}
}

func TestFitWorthy(t *testing.T) {
	raw := detect.TTYState{Known: true}
	line := detect.TTYState{Known: true, Canonical: true, Echo: true}
	prog := treeNode{}
	prog.Term.Meta = core.Meta{Name: "p"}
	shell := treeNode{}
	shell.Term.Meta = core.Meta{Name: "s", Shell: "/bin/zsh"}
	busy := shell
	busy.Pending = "claude"
	for _, tc := range []struct {
		name   string
		n      treeNode
		s      mux.ScreenInfo
		tty    detect.TTYState
		fitted bool
		want   bool
	}{
		{"alt screen anywhere", shell, mux.ScreenInfo{AltScreen: true}, line, false, true},
		{"program with raw tty", prog, mux.ScreenInfo{}, raw, false, true},
		{"program hiding its cursor, tty unknown", prog, mux.ScreenInfo{CursorHidden: true}, detect.TTYState{}, false, true},
		{"program in line mode, cursor shown", prog, mux.ScreenInfo{}, line, false, false},
		{"shell at its prompt", shell, mux.ScreenInfo{}, raw, false, false},
		{"shell running a spinner", busy, mux.ScreenInfo{CursorHidden: true}, line, false, false},
		{"shell running an inline TUI", busy, mux.ScreenInfo{CursorHidden: true}, raw, false, true},
		{"shell running a mouse TUI", busy, mux.ScreenInfo{Mouse: true}, raw, false, true},
		{"fitted TUI briefly shows its cursor", busy, mux.ScreenInfo{}, raw, true, true},
		{"fitted shell back at its prompt", shell, mux.ScreenInfo{}, raw, true, false},
		{"tty unknown: shell only by alt screen", busy, mux.ScreenInfo{CursorHidden: true}, detect.TTYState{}, false, false},
	} {
		if got := fitWorthy(tc.n, tc.s, tc.tty, tc.fitted); got != tc.want {
			t.Errorf("%s: fitWorthy = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The content area a program is fitted to is exactly what the preview box
// shows of it: every captured row and column is drawn, none is cut.
func TestFittedScreenFillsThePreview(t *testing.T) {
	f := newFitMux()
	a := fitApp(t, f)
	for _, size := range [][2]int{{160, 40}, {110, 30}, {90, 50}, {250, 60}} {
		a.w, a.h = size[0], size[1]
		cols, rows := a.previewContent()
		a.preview = nil
		for i := 0; i < rows; i++ {
			a.preview = append(a.preview, fmt.Sprintf("%-*s", cols, fmt.Sprintf("row%02d", i)))
		}
		a.preview[rows-1] = a.preview[rows-1][:cols-1] + "Z"
		a.draw()
		lines := frameLines(a.out.(*bytes.Buffer))
		frame := strings.Join(lines, "\n")
		if !strings.Contains(frame, "row00") || !strings.Contains(frame, "Z│") {
			t.Errorf("%dx%d: a fitted %dx%d screen is cut:\n%s", size[0], size[1], cols, rows, frame)
		}
	}
}
