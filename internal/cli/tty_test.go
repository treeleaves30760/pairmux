package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/muxd"
	"github.com/treeleaves30760/pairmux/internal/output"
)

func TestIsTerminalRejectsPipesAndFiles(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) || isTerminal(w) {
		t.Error("a pipe was taken for a terminal")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("a regular file was taken for a terminal")
	}
}

// A console that cannot host the TUI (muxd.ErrNoTUI) gets a usage error from
// pairmux tui, and runTUI hands the error back without printing, so a bare ls
// can print its listing instead.
func TestTUIUnsupportedConsole(t *testing.T) {
	withTTY(t, true, false)
	old := tuiRunHook
	t.Cleanup(func() { tuiRunHook = old })
	tuiRunHook = func(*tuiApp) error { return fmt.Errorf("%w (SetConsoleMode: invalid parameter)", muxd.ErrNoTUI) }

	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc, err := c.runTUI(time.Second); !errors.Is(err, muxd.ErrNoTUI) || rc != 0 || buf.Len() != 0 {
		t.Fatalf("runTUI = %d, %v, printed %q", rc, err, buf.String())
	}
	if rc := c.cmdTUI(nil); rc != 1 {
		t.Fatalf("cmdTUI rc = %d", rc)
	}
	if e := decode(t, &buf); e.Error == nil || e.Error.Code != output.CodeBadArgs || !strings.Contains(e.Error.Message, "VT input") {
		t.Fatalf("envelope = %+v", e)
	}
}
