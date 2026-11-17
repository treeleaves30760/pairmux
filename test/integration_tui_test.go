//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The TUI's boxes keep their right edge in the terminal's last column. v1.3.0
// lost it: with autowrap off the cursor stays on the last column after a
// full-width row, and the erase-to-end-of-line written after each row wiped
// that cell. This runs the real binary's tui in a detached tmux pane and reads
// the screen back with capture-pane.
func TestTUIRightBorderInTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmp, err := os.MkdirTemp("", "pmxtui") // short: tmux socket paths are length-limited
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	home := filepath.Join(tmp, "home")
	os.MkdirAll(home, 0o700)
	server := "pmx-it-tui-" + strconv.Itoa(os.Getpid())
	tmux := func(args ...string) (string, error) {
		cmd := exec.Command("tmux", append([]string{"-L", server, "-f", "/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+tmp, "HOME="+home)
		cmd.Env = append(cmd.Env, "TMUX=") // never talk to an enclosing tmux
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	defer tmux("kill-server")

	const w, h = 90, 16
	// The pane's command is a person's shell: no agent or pairmux-scope markers,
	// its own HOME, the built-in backend so the TUI starts no nested tmux.
	inner := strings.Join([]string{
		"env", "-u", "CLAUDECODE", "-u", "PAIRMUX_SCOPE", "-u", "PAIRMUX_NAME", "-u", "CODEX_SANDBOX",
		"-u", "PAIRMUX_TUI_AMBIGUOUS_WIDTH",
		"HOME=" + home, "PAIRMUX_BACKEND=builtin", "PAIRMUX_STATE_DIR=" + filepath.Join(tmp, "state"),
		"PAIRMUX_SOCKET=pmx-it-tui", "PAIRMUX_TMPDIR=" + filepath.Join(tmp, "run"),
		binPath, "tui",
	}, " ") + "; sleep 30"
	if out, err := tmux("new-session", "-d", "-x", strconv.Itoa(w), "-y", strconv.Itoa(h), inner); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}

	var rows []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := tmux("capture-pane", "-p")
		rows = strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(rows) > 3 && strings.Contains(rows[1], "terminals") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(rows) < h-1 || !strings.Contains(rows[1], "terminals") {
		t.Fatalf("the TUI did not draw:\n%s", strings.Join(rows, "\n"))
	}
	tmux("send-keys", "q")

	last := func(s string) string {
		r, _ := utf8.DecodeLastRuneInString(s)
		return string(r)
	}
	if got := last(rows[1]); got != "╮" {
		t.Errorf("top row ends with %q, want ╮ (%d cells):\n%s", got, utf8.RuneCountInString(rows[1]), rows[1])
	}
	if got := last(rows[2]); got != "│" { // the list box's column header row
		t.Errorf("middle row ends with %q, want │ (%d cells):\n%s", got, utf8.RuneCountInString(rows[2]), rows[2])
	}
	for y := 1; y < h-1; y++ {
		if got := last(rows[y]); !strings.Contains("╮│╯", got) {
			t.Errorf("row %d ends with %q, not a box edge:\n%s", y, got, rows[y])
		}
		if n := utf8.RuneCountInString(rows[y]); n != w {
			t.Errorf("row %d is %d cells, want %d:\n%s", y, n, w, rows[y])
		}
	}
}
