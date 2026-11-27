//go:build integration

package integration

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

// sizeScript is a full-screen program that redraws on SIGWINCH and prints
// the size it was given, so a test can read back what the pane is.
const sizeScript = `printf '\033[?1049h\033[?25l'
draw() { set -- $(stty size); printf '\033[H\033[2J'; printf 'PROG rows=%s cols=%s' "$1" "$2"; }
trap draw WINCH
draw
while :; do sleep 0.05; done
`

var progSizeRE = regexp.MustCompile(`PROG rows=(\d+) cols=(\d+)`)

// hostTmux is a tmux server on its own socket file that hosts what a human
// would run in their terminal (the TUI, an attach), so a test controls that
// terminal's size and keys and reads its screen back.
type hostTmux struct {
	t    *testing.T
	sock string
}

func newHost(t *testing.T) *hostTmux {
	t.Helper()
	dir, err := os.MkdirTemp("", "pmxh") // short: socket paths are length-limited
	if err != nil {
		t.Fatal(err)
	}
	h := &hostTmux{t: t, sock: filepath.Join(dir, "s")}
	t.Cleanup(func() {
		h.run("kill-server")
		os.RemoveAll(dir)
	})
	return h
}

func (h *hostTmux) run(args ...string) (string, error) {
	cmd := exec.Command("tmux", append([]string{"-S", h.sock, "-f", "/dev/null"}, args...)...)
	cmd.Env = append(withoutVar(os.Environ(), "TMUX"), "TMUX=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func withoutVar(env []string, key string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// start runs argv in a new w x h host session, as a person's shell would:
// no agent markers, e's pairmux environment.
func (h *hostTmux) start(e tenv, w, hgt int, argv ...string) {
	h.t.Helper()
	env := []string{"env", "-u", "CLAUDECODE", "-u", "PAIRMUX_SCOPE", "-u", "PAIRMUX_NAME", "-u", "CODEX_SANDBOX", "-u", "TMUX",
		"-u", "PAIRMUX_TUI_AMBIGUOUS_WIDTH",
		"PAIRMUX_STATE_DIR=" + e.state, "PAIRMUX_SOCKET=" + e.socket, "HOME=" + e.home, "PAIRMUX_BACKEND=" + itBackend}
	if e.tmuxTmp != "" {
		env = append(env, runtimeRootEnv(e)...)
	}
	cmd := strings.Join(append(env, argv...), " ") + "; sleep 60"
	if out, err := h.run("new-session", "-d", "-x", strconv.Itoa(w), "-y", strconv.Itoa(hgt), cmd); err != nil {
		h.t.Fatalf("host new-session: %v\n%s", err, out)
	}
}

func (h *hostTmux) keys(k ...string) {
	if out, err := h.run(append([]string{"send-keys"}, k...)...); err != nil {
		h.t.Fatalf("host send-keys: %v\n%s", err, out)
	}
}

func (h *hostTmux) screen() string {
	out, _ := h.run("capture-pane", "-p")
	return out
}

// progSize is the size the program in terminal name last drew for.
func progSize(t *testing.T, e tenv, name string) string {
	t.Helper()
	env, _ := pmx(t, e, "peek", name, "--screen")
	m := progSizeRE.FindStringSubmatch(env.Output)
	if m == nil {
		return "?"
	}
	return m[2] + "x" + m[1]
}

func waitSize(t *testing.T, e tenv, name, want string, h *hostTmux) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	got := ""
	for time.Now().Before(deadline) {
		if got = progSize(t, e, name); got == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s drew for %s, want %s; host screen:\n%s", name, got, want, h.screen())
}

// The TUI fits a full-screen program's pane to its preview box while it is
// selected, never a shell's, gives an attach the human's whole terminal, fits
// again on return, and gives the pane its size back when it quits.
func TestTUIFitsAFullScreenProgram(t *testing.T) {
	if !haveTmux {
		t.Skip("tmux hosts the TUI's terminal")
	}
	e := newEnv(t, "/bin/sh")
	script := filepath.Join(t.TempDir(), "prog.sh")
	if err := os.WriteFile(script, []byte(sizeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	// Listed in name order: prog first, then sh.
	if env, code := pmx(t, e, "new", "--name", "prog", "--cmd", "/bin/sh "+script); code != 0 {
		t.Fatalf("new prog: %+v", env)
	}
	if env, code := pmx(t, e, "new", "--name", "sh"); code != 0 {
		t.Fatalf("new sh: %+v", env)
	}
	waitSize(t, e, "prog", "220x50", nil)

	const w, hgt = 160, 40
	h := newHost(t)
	h.start(e, w, hgt, binPath, "tui")
	// The preview's content area: the box right of a 38% list, inside its
	// border and margin, below the command and meta lines.
	listW := w * 38 / 100
	fit := fmt.Sprintf("%dx%d", w-listW-3, hgt-2-2-2)
	waitSize(t, e, "prog", fit, h)

	// Moving to the shell gives prog its size back and leaves the shell wide.
	h.keys("j")
	waitSize(t, e, "prog", "220x50", h)
	time.Sleep(fitSettleWait)
	if env, _ := pmx(t, e, "run", "sh", "stty size"); !strings.Contains(env.Output, "50 220") {
		t.Fatalf("the shell was resized: %q", env.Output)
	}
	h.keys("k")
	waitSize(t, e, "prog", fit, h)

	// Attach: the whole host terminal (tmux keeps a row for its status line).
	h.keys("Enter")
	full := fmt.Sprintf("%dx%d", w, hgt)
	if itBackend == mux.BackendTmux {
		full = fmt.Sprintf("%dx%d", w, hgt-1)
	}
	waitSize(t, e, "prog", full, h)
	h.keys("C-b", "d")
	waitSize(t, e, "prog", fit, h)

	h.keys("q")
	if itBackend == mux.BackendTmux {
		// tmux keeps a window at the size its last client had.
		waitSize(t, e, "prog", full, h)
	} else {
		waitSize(t, e, "prog", "220x50", h)
	}
}

// fitSettleWait comfortably exceeds the TUI's settle delay and a refresh.
const fitSettleWait = 1500 * time.Millisecond

// On a real tmux: a fit sizes the window and makes its size manual, unfit
// restores both, a fit is refused while a client is attached, and a fit left
// behind by a dead TUI is released so an attaching client gets its own size.
func TestTmuxFitPane(t *testing.T) {
	requireTmuxBackend(t)
	t.Setenv("TMUX", "")
	socket := fmt.Sprintf("pmx-it-fit-%d", os.Getpid())
	c := tmux.New(socket)
	defer c.KillServer()
	if err := c.EnsureSession(); err != nil {
		t.Fatal(err)
	}
	raw := func(args ...string) string {
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	pane := raw("new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "w", "sh", "-c", sizeScript)
	windowSize := func() string { return raw("display", "-p", "-t", pane, "#{window_width}x#{window_height}") }
	manual := func() string { return raw("show-options", "-w", "-t", pane, "window-size") }
	drawn := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			s, _ := c.CapturePane(pane, 0)
			m := progSizeRE.FindStringSubmatch(s)
			if m != nil && m[2]+"x"+m[1] == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("program drew %q, want %s", s, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	drawn("220x50")

	s, err := c.PaneScreen(pane)
	if err != nil || s.Cols != 220 || s.Rows != 50 || !s.AltScreen || !s.CursorHidden || s.Fitted || s.Attached != 0 {
		t.Fatalf("PaneScreen = %+v, %v", s, err)
	}
	if err := c.FitPane(pane, 97, 34); err != nil {
		t.Fatal(err)
	}
	if got := windowSize(); got != "97x34" || manual() != "window-size manual" {
		t.Fatalf("fitted: %s, %q", got, manual())
	}
	drawn("97x34")
	if err := c.FitPane(pane, 80, 30); err != nil { // a resized TUI
		t.Fatal(err)
	}
	if err := c.UnfitPane(pane); err != nil {
		t.Fatal(err)
	}
	if got := windowSize(); got != "220x50" || manual() != "" {
		t.Fatalf("unfitted: %s, %q (the size from before the first fit, window-size inherited)", got, manual())
	}
	drawn("220x50")

	// Another process (a TUI that died) fitted it; a live owner's fit stays.
	if err := c.FitPane(pane, 97, 34); err != nil {
		t.Fatal(err)
	}
	if err := c.ReleaseFits(func(int) bool { return true }); err != nil || windowSize() != "97x34" {
		t.Fatalf("a live owner's fit was released: %v %s", err, windowSize())
	}
	raw("set-option", "-w", "-t", pane, "@pairmux_fit_pid", "999999")
	if err := c.ReleaseFits(func(pid int) bool { return pid != 999999 }); err != nil {
		t.Fatal(err)
	}
	if got := windowSize(); got != "220x50" || manual() != "" {
		t.Fatalf("a dead owner's fit was kept: %s %q", got, manual())
	}

	// A client attached: no fit, and an attach after a fit gets its own size
	// once the fit is released (what pairmux attach does first).
	if err := c.FitPane(pane, 97, 34); err != nil {
		t.Fatal(err)
	}
	if err := c.ReleaseFits(nil); err != nil {
		t.Fatal(err)
	}
	h := newHost(t)
	cmd := "tmux -L " + socket + " attach -t pairmux:w"
	if out, err := h.run("new-session", "-d", "-x", "150", "-y", "40", "env -u TMUX "+cmd); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for windowSize() != "150x39" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := windowSize(); got != "150x39" {
		t.Fatalf("attached window is %s, want the client's 150x39", got)
	}
	if err := c.FitPane(pane, 97, 34); !errors.Is(err, mux.ErrPaneWatched) {
		t.Fatalf("fit while attached = %v", err)
	}
	if got := windowSize(); got != "150x39" {
		t.Fatalf("attached window resized to %s", got)
	}
}
