// Package mux is the seam between pairmux and the terminal multiplexer that
// owns its panes. pairmux needs very little from a multiplexer — create a pane,
// stream its output into a file, type into it, read its screen, list and kill
// panes — and that surface is declared here once so the CLI and the registry
// do not care which implementation answers.
//
// Two implementations exist: package tmux drives an external tmux server, and
// package muxd is pairmux's own built-in server (a PTY on Unix, a ConPTY on
// Windows), which is what lets pairmux run with no tmux installed and on
// native Windows. Package backend chooses between them.
package mux

import (
	"errors"
	"regexp"
	"time"
)

// Backend names, as reported by Mux.Backend and accepted by PAIRMUX_BACKEND.
const (
	BackendTmux    = "tmux"
	BackendBuiltin = "builtin"
)

// PaneInfo describes one managed pane.
type PaneInfo struct {
	PaneID     string
	Window     string
	Name       string
	CurrentCmd string
	Dead       bool
}

// NewWindowReq describes a window (one terminal) to create.
type NewWindowReq struct {
	Name string            // window name
	Dir  string            // cwd; "" inherits
	Env  map[string]string // added to (and overriding) the calling process's environment
	Argv []string          // command; empty runs the default shell
	// Gate is a FIFO the pane blocks on before the command starts. tmux runs a
	// pane's command the moment the window exists, which is before pipe-pane can
	// be attached to it — anything printed in that window is never journalled,
	// and a program whose first act is to prompt can therefore be invisible to
	// pairmux entirely. Holding the pane on a read the caller releases after
	// wiring the journal removes the race rather than narrowing it. Ignored when
	// Argv is empty: a shell prints nothing until its rc files run, and hooks
	// mode already nudges a prompt cycle to cover the same gap.
	//
	// A backend that reports AtomicJournal does not need a gate and ignores it.
	Gate string
	// EnvFile is where a backend that cannot hand the pane its environment
	// directly (tmux, whose panes otherwise inherit the server's stale global
	// environment) writes it for the pane to pick up: a path in a directory
	// private to the user, normally the terminal's own. The pane consumes and
	// removes it; the caller removes it if creation is abandoned. Required
	// by such a backend (it refuses ""). Backends that set the environment directly
	// ignore it.
	EnvFile string
	// Journal, when set, is the file the pane's output is appended to from the
	// first byte. Only backends that report AtomicJournal honour it; others
	// need PipePaneAppend after the window exists.
	Journal string
}

// Mux is everything pairmux asks of a terminal multiplexer. Every method names
// its pane by the id NewWindow returned ("%N"); names are pairmux's own and
// live in the @pairmux_name pane option.
type Mux interface {
	// Backend reports which implementation this is (BackendTmux/BackendBuiltin).
	Backend() string
	// SocketName is the endpoint name (tmux -L, or the built-in server's
	// socket), which also keys pairmux's state namespace.
	SocketName() string
	// Version returns the multiplexer's version token.
	Version() (string, error)
	// AtomicJournal reports whether NewWindowReq.Journal is honoured, so the
	// caller can skip the start gate and the separate PipePaneAppend.
	AtomicJournal() bool

	EnsureSession() error
	NewWindow(r NewWindowReq) (string, error)
	PipePaneAppend(paneID, file string) error
	SendLiteral(paneID, text string) error
	SendKeys(paneID string, keys ...string) error
	CapturePane(paneID string, historyLines int) (string, error)
	SetPaneOption(paneID, opt, val string) error
	ListManaged() ([]PaneInfo, error)
	KillWindowOf(paneID string) error
	SelectWindow(name string) error
	HasSession() bool
	KillServer() error
	PaneTTY(paneID string) (string, error)
	PaneAudience(paneID string) (attached int, active bool, err error)

	// PaneScreen reports a pane's size and the screen modes its program has
	// set. ErrFitUnsupported when the multiplexer cannot tell (a built-in
	// server started by an older pairmux).
	PaneScreen(paneID string) (ScreenInfo, error)
	// FitPane gives a pane nobody is attached to a temporary size: the human's
	// TUI preview box, so a full-screen program redraws to fit what the human
	// is shown. The size the pane had before the first FitPane is remembered
	// by the multiplexer itself (UnfitPane restores it), so a later fit, or a
	// fit from another TUI, never mistakes a preview size for the real one.
	// Attached humans always own the size: FitPane returns ErrPaneWatched
	// while anyone is attached, and an attach ends the fit. ErrFitUnsupported
	// means this multiplexer cannot do it; callers just skip the fit.
	//
	// On the built-in backend a fit is a lease that lapses on its own unless
	// renewed (FitLease), so a TUI that dies cannot leave a pane small.
	FitPane(paneID string, cols, rows int) error
	// UnfitPane ends a fit: the pane gets back the size it had before, or, if
	// a human attached meanwhile, simply keeps following their terminal. A
	// pane that is not fitted is left alone.
	UnfitPane(paneID string) error
}

// ScreenInfo is a pane's size and the modes that tell a full-screen,
// self-redrawing program from a line-oriented one.
type ScreenInfo struct {
	Cols, Rows   int
	AltScreen    bool // the program switched to the alternate screen
	CursorHidden bool // the program hid the terminal cursor (?25l)
	Mouse        bool // the program asked for mouse reports
	Attached     int  // humans attached (tmux: clients on the session)
	Fitted       bool // a FitPane is in effect
}

// FitLease is how long a built-in fit lasts without being renewed. A TUI
// renews the fit of the pane it previews well within it.
const FitLease = 20 * time.Second

var (
	// ErrFitUnsupported reports a multiplexer that cannot fit panes.
	ErrFitUnsupported = errors.New("pane fitting is not supported by this multiplexer")
	// ErrPaneWatched reports a pane a human is attached to: its size is theirs.
	ErrPaneWatched = errors.New("a human is attached to this pane")
)

var socketNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ValidSocketName reports whether socket is safe to use as an endpoint name.
// tmux accepts path separators (including ../) in -L, and the built-in server
// turns the name into a file name, so argv separation alone does not prevent
// directory traversal.
func ValidSocketName(socket string) bool { return socketNameRe.MatchString(socket) }

// FitSweeper is implemented by multiplexers whose fits outlive the process
// that made them (tmux, where a fit is a window option): ReleaseFits ends
// every fit whose owner keep does not vouch for (nil keep: every fit). The
// built-in server needs none — its fits are leases that lapse on their own.
type FitSweeper interface {
	ReleaseFits(keep func(owner int) bool) error
}
