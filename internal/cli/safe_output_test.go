package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// hostileText is what a note or a command line can carry: a clipboard write,
// then cursor-up / erase-line / carriage return to replace the row above it.
const hostileText = "ok\x1b]52;c;cm0gLXJmIH4=\x07\x1b[1A\x1b[2K\rall good"

// controlsIn returns the control characters in s other than the ones a frame
// is allowed to contain.
func controlsIn(s string, allowed string) string {
	var bad []rune
	for _, r := range s {
		if (r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f) && !strings.ContainsRune(allowed, r) {
			bad = append(bad, r)
		}
	}
	return string(bad)
}

func TestTUIRenderNeutralisesStoredText(t *testing.T) {
	a := &tuiApp{noColor: true}
	got := a.render(tline{{" ✉ note: " + hostileText, ""}}, 200, "")
	if bad := controlsIn(got, ""); bad != "" {
		t.Fatalf("render let control characters %q through: %q", bad, got)
	}
	if !strings.Contains(got, `\x1b]52;c;cm0gLXJmIH4=\x07`) {
		t.Fatalf("render should show the sequence as text: %q", got)
	}
	if strWidth(got) != 200 {
		t.Fatalf("render width = %d, want 200", strWidth(got))
	}
	// Styled output keeps pairmux's own SGR and nothing else.
	styled := (&tuiApp{}).render(tline{{hostileText, stDim}}, 80, "")
	inner := strings.TrimSuffix(strings.TrimPrefix(styled, "\x1b["+stDim+"m"), "\x1b[0m")
	if i := strings.Index(inner, "\x1b[0m"); i >= 0 {
		inner = inner[:i]
	}
	if bad := controlsIn(inner, ""); bad != "" {
		t.Fatalf("styled render let control characters %q through: %q", bad, styled)
	}
}

func TestActivityViewNeutralisesNotesAndCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build")
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []core.Event{
		{Type: core.EvCmdStart, CmdID: 1, Text: "make test \x1b[2J"},
		{Type: core.EvNote, Text: hostileText},
	} {
		if err := j.AppendEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	n := treeNode{Path: "build", Term: state.Terminal{Name: "build", Dir: dir}}
	a := &tuiApp{noColor: true}
	var frame strings.Builder
	for _, line := range activityLines(n) {
		frame.WriteString(a.render(tline{{" " + line, ""}}, 300, ""))
		frame.WriteByte('\n')
	}
	if bad := controlsIn(frame.String(), "\n"); bad != "" {
		t.Fatalf("activity view let control characters %q through:\n%q", bad, frame.String())
	}
	for _, want := range []string{`make test \x1b[2J`, `\x1b]52;c;cm0gLXJmIH4=\x07`} {
		if !strings.Contains(frame.String(), want) {
			t.Errorf("activity view lacks %q", want)
		}
	}
}

func TestWatchTableNeutralisesPendingCommand(t *testing.T) {
	var b strings.Builder
	writeWatchTable(&b, []watchRow{{name: "build", status: core.StatusRunning, mode: "hooks", cmd: "sleep 9 #\x1b[1A\x1b[2K\rnothing to see"}})
	if bad := controlsIn(b.String(), "\n"); bad != "" {
		t.Fatalf("watch table let control characters %q through: %q", bad, b.String())
	}
	if !strings.Contains(b.String(), `sleep 9 #\x1b[1A\x1b[2K\rnothing to see`) {
		t.Fatalf("watch table should show the command as text: %q", b.String())
	}
}

// The human block of every command goes through one emitter.
func TestHumanEnvelopeNeutralisesNotes(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, false)
	c.emit(output.Envelope{Status: "idle", Terminal: "build", Notes: []string{hostileText}})
	if bad := controlsIn(buf.String(), "\n"); bad != "" {
		t.Fatalf("human envelope let control characters %q through: %q", bad, buf.String())
	}
}
