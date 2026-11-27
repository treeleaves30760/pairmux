package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/treeleaves30760/pairmux/internal/detect"
	"github.com/treeleaves30760/pairmux/internal/mux"
)

// This file fits the previewed terminal to the preview box.
//
// Terminals are 220x50 so agents' command output does not wrap. A preview box
// is about half the human's screen, so a full-screen program — Claude Code,
// Codex, vim, htop — drawn for 220x50 and cut into it shows its top-left
// corner and nothing of its layout. While such a program's terminal is the
// one the human has selected, its pane is resized to the box's content size
// (mux.Mux.FitPane) and the program redraws to fit; attaching gives it the
// human's whole terminal, as always.
//
// What is never resized:
//   - a terminal that is not running a full-screen program (see fitWorthy):
//     agents run line commands in shells and rely on the wide screen, and a
//     human moving the selection must not send their builds a SIGWINCH;
//   - a pane a human is attached to: attached humans own the size;
//   - a dead pane, or any pane while the preview is too small to be useful,
//     shows the activity tab, or is covered by the full-screen reader.
//
// A fit is applied only once the selection has rested for fitSettle, so
// holding j through the list resizes nothing it passes over. Moving on
// returns the previous pane to its size, and so does quitting (also on
// SIGTERM/SIGHUP/SIGINT and on a panic). Should the TUI die anyway, the
// built-in server's fits are leases that lapse by themselves, and tmux fits
// are released by the next TUI that starts and by every pairmux attach.

const (
	// fitSettle is how long the selection must rest before its pane is fitted.
	fitSettle = 300 * time.Millisecond
	// fitRenew bounds the time between re-evaluations of a fitted pane, which
	// also renew the built-in server's lease (mux.FitLease) well within it.
	fitRenew = 5 * time.Second
	// minFitCols, minFitRows: below this a fitted program is too cramped to
	// be worth a resize; the preview then shows the clipped wide screen.
	minFitCols = 40
	minFitRows = 10
)

// fitRedraws are when, after a resize, the preview is recaptured: the program
// needs a moment to receive SIGWINCH and repaint. The first catches a quick
// redraw; the second one that is slow or was mid-frame.
var fitRedraws = []time.Duration{120 * time.Millisecond, 450 * time.Millisecond}

type fitKey struct{ socket, pane string }

type fitState struct {
	fitted      map[fitKey][2]int // panes this TUI fitted, and the size it gave each
	unsupported map[string]bool   // endpoints that cannot fit (old server, old tmux)
	target      string            // what the last evaluation was for (selection + geometry)
	due         time.Time         // when the rested selection is evaluated
	renew       time.Time         // when a fitted pane is next re-evaluated
	recapture   []time.Time       // when the preview is next recaptured
}

// muxFor is the multiplexer of endpoint socket.
func (a *tuiApp) muxFor(socket string) mux.Mux {
	if a.layerFn != nil {
		return a.layerFn(socket)
	}
	if socket == a.c.Mux.SocketName() {
		return a.c.Mux
	}
	return a.c.layer(socket)
}

// previewContent is the size of the preview box's content area: the cells
// below the command and meta lines, inside the border and the one-cell
// margin. It is what a fitted program is given, so it must follow drawPreview.
func (a *tuiApp) previewContent() (cols, rows int) {
	w, h := a.previewBox()
	if h < 3 {
		return 0, 0
	}
	cmd, meta := previewHeads(h)
	return w - 3, h - 2 - boolInt(cmd) - boolInt(meta)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fitTargetKey identifies what a fit decision depends on: the selected live
// pane and the preview's geometry and visibility. When it changes the
// decision is made again, once the selection has rested.
func (a *tuiApp) fitTargetKey() string {
	vis := a.visible()
	pane, socket := "", ""
	if a.sel >= 0 && a.sel < len(vis) {
		n := vis[a.sel].node
		if n.Term.Alive {
			socket, pane = n.Socket, n.Term.PaneID
		}
	}
	cols, rows := a.previewContent()
	return fmt.Sprintf("%s|%s|%dx%d|%d|%v", socket, pane, cols, rows, a.view, a.viewer != nil)
}

// noteFitTarget restarts the settle delay when the fit target changed.
func (a *tuiApp) noteFitTarget(now time.Time) {
	if k := a.fitTargetKey(); k != a.fit.target {
		a.fit.target = k
		a.fit.due = now.Add(fitSettle)
	}
}

// fitWake is the earliest fit timer, or zero.
func (a *tuiApp) fitWake() time.Time {
	var t time.Time
	for _, c := range append([]time.Time{a.fit.due, a.fit.renew}, a.fit.recapture...) {
		if !c.IsZero() && (t.IsZero() || c.Before(t)) {
			t = c
		}
	}
	return t
}

// fitTimers runs whatever fit timers are due.
func (a *tuiApp) fitTimers(now time.Time) {
	if (!a.fit.due.IsZero() && !now.Before(a.fit.due)) || (!a.fit.renew.IsZero() && !now.Before(a.fit.renew)) {
		a.fit.due = time.Time{}
		a.applyFit(now)
	}
	recap := false
	keep := a.fit.recapture[:0]
	for _, t := range a.fit.recapture {
		if now.Before(t) {
			keep = append(keep, t)
		} else {
			recap = true
		}
	}
	a.fit.recapture = keep
	if recap {
		a.loadPreview()
	}
}

// settled reports that no evaluation is pending, so a refresh tick may
// re-evaluate the rested selection (a program started or ended in it).
func (a *tuiApp) fitSettled() bool { return a.fit.due.IsZero() }

// applyFit makes the selected pane fitted if it should be, and every other
// pane this TUI fitted unfitted.
func (a *tuiApp) applyFit(now time.Time) {
	a.fit.renew = time.Time{}
	if a.fit.fitted == nil {
		a.fit.fitted = map[fitKey][2]int{}
	}
	var target *treeNode
	vis := a.visible()
	cols, rows := a.previewContent()
	if a.sel >= 0 && a.sel < len(vis) && a.viewer == nil && a.view == viewScreen && cols >= minFitCols && rows >= minFitRows {
		if n := vis[a.sel].node; n.Term.Alive && n.Term.PaneID != "" && !a.fit.unsupported[n.Socket] {
			target = &n
		}
	}
	var key fitKey
	if target != nil {
		key = fitKey{target.Socket, target.Term.PaneID}
	}
	for k := range a.fit.fitted {
		if target == nil || k != key {
			a.unfit(k)
		}
	}
	if target == nil {
		return
	}
	m := a.muxFor(key.socket)
	info, err := m.PaneScreen(key.pane)
	if err != nil {
		if errors.Is(err, mux.ErrFitUnsupported) {
			a.fitUnsupported(key.socket)
		}
		delete(a.fit.fitted, key) // gone, or unreadable: nothing left to undo
		return
	}
	_, mine := a.fit.fitted[key]
	if info.Attached > 0 || !fitWorthy(*target, info, ttyStateOf(*target), mine && info.Fitted) {
		if mine {
			a.unfit(key)
		}
		return
	}
	if err := m.FitPane(key.pane, cols, rows); err != nil {
		if errors.Is(err, mux.ErrFitUnsupported) {
			a.fitUnsupported(key.socket)
		}
		delete(a.fit.fitted, key)
		return
	}
	a.fit.fitted[key] = [2]int{cols, rows}
	a.fit.renew = now.Add(fitRenew)
	if info.Cols != cols || info.Rows != rows {
		a.fit.recapture = a.fit.recapture[:0]
		for _, d := range fitRedraws {
			a.fit.recapture = append(a.fit.recapture, now.Add(d))
		}
	}
}

func (a *tuiApp) fitUnsupported(socket string) {
	if a.fit.unsupported == nil {
		a.fit.unsupported = map[string]bool{}
	}
	a.fit.unsupported[socket] = true
}

// unfit gives one pane back the size it had before this TUI fitted it.
// Failures are ignored: the pane may have been killed, and a lapsed lease or
// an attach has undone the fit already.
func (a *tuiApp) unfit(k fitKey) {
	delete(a.fit.fitted, k)
	_ = a.muxFor(k.socket).UnfitPane(k.pane)
}

// unfitAll undoes every fit this TUI made: on quit, and before an attach.
func (a *tuiApp) unfitAll() {
	for k := range a.fit.fitted {
		a.unfit(k)
	}
	a.fit.renew, a.fit.recapture = time.Time{}, nil
}

// forgetFit drops a pane from the TUI's fits without undoing it: an attach
// takes the pane's size over itself (the built-in server ends the fit,
// pairmux attach on tmux releases it).
func (a *tuiApp) forgetFit(socket, pane string) {
	delete(a.fit.fitted, fitKey{socket, pane})
}

// releaseStaleFits undoes tmux fits left behind by TUIs that are no longer
// running, on every endpoint the tree shows.
func (a *tuiApp) releaseStaleFits() {
	seen := map[string]bool{}
	for _, n := range append([]treeNode{{Socket: a.c.Mux.SocketName()}}, a.nodes...) {
		if seen[n.Socket] {
			continue
		}
		seen[n.Socket] = true
		if fs, ok := a.muxFor(n.Socket).(mux.FitSweeper); ok {
			_ = fs.ReleaseFits(processAlive)
		}
	}
}

// ttyStateOf reads the line discipline of a terminal's pane, when known.
var ttyStateOf = func(n treeNode) detect.TTYState { return detect.ReadTTYState(n.Term.Meta.Tty) }

// fitWorthy is the rule for "this terminal is running a full-screen,
// self-redrawing program", from what the multiplexer and the pane's terminal
// say rather than from program names — Claude Code shows up as claude, node
// or a version number, and an agent can be launched under any wrapper.
//
//   - The alternate screen is on: vim, less, htop, top, most TUIs. Always.
//   - A program terminal (pairmux new x -- prog, Meta.Shell == "") whose
//     program took the keyboard (raw tty), hid the cursor to draw its own, or
//     asked for mouse reports. Claude Code (Ink) and Codex draw inline on the
//     main screen, so the alternate screen alone would miss them; they do
//     all three.
//   - A shell terminal whose foreground job both took the keyboard and hid
//     the cursor or wants the mouse: claude typed at the shell. A shell at its
//     prompt reads raw keys too, but shows its cursor; a build with a spinner
//     hides the cursor, but leaves the tty in line mode. Neither is fitted.
//
// Once fitted, a pane stays fitted while its program keeps any of these
// signs, so a program that briefly shows its cursor is not resized back and
// forth; a shell terminal is let go when its command has ended.
func fitWorthy(n treeNode, s mux.ScreenInfo, tty detect.TTYState, fitted bool) bool {
	if s.AltScreen {
		return true
	}
	draws := s.CursorHidden || s.Mouse
	if n.Term.Meta.Shell == "" && n.Term.Meta.Name != "" { // a program terminal
		return draws || tty.RawInput()
	}
	if fitted {
		return draws || tty.RawInput() && n.Pending != ""
	}
	return draws && tty.RawInput()
}
