package tmux

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/treeleaves30760/pairmux/internal/mux"
)

// Fitting a pane to a human's TUI preview (mux.Mux.FitPane) on tmux.
//
// tmux sizes a window from its attached clients ("window-size latest"). With
// nobody attached it keeps whatever size it last had, which for pairmux is the
// 220x50 the session was created with. resize-window -x/-y is the only way to
// size a window nobody is attached to, and it has a side effect that matters:
// it switches that window's window-size option to "manual" (so does -A), after
// which an attaching client no longer resizes the window — the human would get
// the small preview size in their full-screen attach. Every fit therefore ends
// by unsetting the window's window-size, which makes it follow its clients
// again (immediately, if one is attached; verified against tmux 3.7b).
//
// What a fit changed is recorded on the window itself, in two user options, so
// that any pairmux process can undo it — the TUI that made it, a later TUI
// whose predecessor crashed, or `pairmux attach`:
//
//	@pairmux_fit      the size before the first fit, "COLSxROWS"
//	@pairmux_fit_pid  the process that owns the fit (a TUI)
//
// resize-window -x/-y and window-scoped user options exist since tmux 2.9,
// older than the 3.2 pairmux requires.
const (
	optFit    = "@pairmux_fit"
	optFitPID = "@pairmux_fit_pid"
)

const screenFormat = "#{window_width} #{window_height} #{alternate_on} #{cursor_flag} #{mouse_any_flag} #{session_attached} #{pane_dead} #{" + optFit + "}"

// PaneScreen reports the pane's window size, its screen modes and whether a
// fit is in effect. Attached counts the clients on the whole session: one
// tmux session holds every terminal, and an attached human can switch to any
// window at any time.
func (c *Client) PaneScreen(paneID string) (mux.ScreenInfo, error) {
	out, err := c.run("display", "-p", "-t", paneID, "-F", screenFormat)
	if err != nil {
		return mux.ScreenInfo{}, err
	}
	return parseScreen(out)
}

func parseScreen(out string) (mux.ScreenInfo, error) {
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) < 7 {
		return mux.ScreenInfo{}, fmt.Errorf("tmux: unexpected pane screen %q", out)
	}
	cols, err1 := strconv.Atoi(f[0])
	rows, err2 := strconv.Atoi(f[1])
	att, err3 := strconv.Atoi(f[5])
	if err1 != nil || err2 != nil || err3 != nil {
		return mux.ScreenInfo{}, fmt.Errorf("tmux: unexpected pane screen %q", out)
	}
	if f[6] == "1" {
		return mux.ScreenInfo{}, errors.New("tmux: pane is dead")
	}
	return mux.ScreenInfo{
		Cols: cols, Rows: rows,
		AltScreen:    f[2] == "1",
		CursorHidden: f[3] == "0",
		Mouse:        f[4] == "1",
		Attached:     att,
		Fitted:       len(f) > 7 && f[7] != "",
	}, nil
}

// FitPane resizes the pane's window to cols x rows unless a client is
// attached to the session. The first fit records the window's current size;
// later ones (renewals, a resized TUI) keep that record.
func (c *Client) FitPane(paneID string, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("tmux: fit: bad size %dx%d", cols, rows)
	}
	info, err := c.PaneScreen(paneID)
	if err != nil {
		return err
	}
	if info.Attached > 0 {
		return mux.ErrPaneWatched
	}
	pid := strconv.Itoa(os.Getpid())
	var args []string
	if !info.Fitted {
		args = append(args, "set-option", "-w", "-t", paneID, optFit, fmt.Sprintf("%dx%d", info.Cols, info.Rows), ";")
	}
	args = append(args, "set-option", "-w", "-t", paneID, optFitPID, pid)
	if info.Cols != cols || info.Rows != rows {
		args = append(args, ";", "resize-window", "-t", paneID, "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows))
	}
	if _, err := c.run(args...); err != nil {
		if unsupported(err) {
			return mux.ErrFitUnsupported
		}
		return err
	}
	return nil
}

// UnfitPane undoes a fit: the window gets its recorded size back — unless a
// client is attached, whose size it then simply follows — and window-size is
// unset so the next attach sizes it to the human's terminal.
func (c *Client) UnfitPane(paneID string) error {
	out, err := c.run("display", "-p", "-t", paneID, "-F", "#{session_attached} #{"+optFit+"}")
	if err != nil {
		return err
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) < 2 {
		return nil // not fitted
	}
	var args []string
	if f[0] == "0" {
		if w, h, ok := parseSize(f[1]); ok {
			args = append(args, "resize-window", "-t", paneID, "-x", strconv.Itoa(w), "-y", strconv.Itoa(h), ";")
		}
	}
	args = append(args,
		"set-option", "-w", "-u", "-t", paneID, "window-size", ";",
		"set-option", "-w", "-u", "-t", paneID, optFit, ";",
		"set-option", "-w", "-u", "-t", paneID, optFitPID)
	_, err = c.run(args...)
	return err
}

// ReleaseFits ends every fit on the socket whose owner keep does not vouch
// for: a TUI that starts releases those of TUIs that died without cleaning
// up, and attach releases all of them, since an attached human can switch to
// any window of the session.
func (c *Client) ReleaseFits(keep func(owner int) bool) error {
	out, err := c.run("list-panes", "-a", "-F", "#{pane_id} #{"+optFitPID+"} #{"+optFit+"}")
	if err != nil {
		return err
	}
	var firstErr error
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue // not fitted
		}
		pid, _ := strconv.Atoi(f[1])
		if keep != nil && pid > 0 && keep(pid) {
			continue
		}
		if err := c.UnfitPane(f[0]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

var _ mux.FitSweeper = (*Client)(nil)

func parseSize(s string) (int, int, bool) {
	ws, hs, ok := strings.Cut(s, "x")
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// unsupported reports a tmux too old for a command or flag.
func unsupported(err error) bool {
	s := err.Error()
	return strings.Contains(s, "unknown command") || strings.Contains(s, "unknown flag") ||
		strings.Contains(s, "invalid option")
}
