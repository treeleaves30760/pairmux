package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

func TestEveryTerminalNameCommandRejectsTraversal(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Ctx) int
	}{
		{"new", func(c *Ctx) int { return c.cmdNew([]string{"--name", "../escape"}) }},
		{"run", func(c *Ctx) int { return c.cmdRun([]string{"../escape", "echo", "ok"}) }},
		{"peek", func(c *Ctx) int { return c.cmdPeek([]string{"../escape"}) }},
		{"wait", func(c *Ctx) int { return c.cmdWait([]string{"../escape", "--timeout", "1ms"}) }},
		{"send", func(c *Ctx) int { return c.cmdSend([]string{"../escape", "--enter"}) }},
		{"log", func(c *Ctx) int { return c.cmdLog([]string{"../escape"}) }},
		{"kill", func(c *Ctx) int { return c.cmdKill([]string{"../escape"}) }},
		{"attach", func(c *Ctx) int { return c.cmdAttach([]string{"../escape"}) }},
		{"note", func(c *Ctx) int { return c.cmdNote([]string{"../escape", "hello"}) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			c := newTestCtx(&buf, true)
			c.StateDir = t.TempDir()
			if rc := tt.run(c); rc != 1 {
				t.Fatalf("rc = %d, want 1; output=%s", rc, buf.String())
			}
			env := decode(t, &buf)
			if env.OK || env.Error == nil || env.Error.Code != output.CodeBadArgs || !strings.Contains(env.Error.Message, "invalid name") {
				t.Fatalf("envelope = %+v, want invalid-name E_BAD_ARGS", env)
			}
			entries, err := os.ReadDir(c.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("invalid command touched state: %v", entries)
			}
		})
	}
}

func TestStateAndTmuxCommandsRejectInvalidSocketBeforeIO(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Ctx) int
	}{
		{"new", func(c *Ctx) int { return c.cmdNew([]string{"--name", "safe"}) }},
		{"run", func(c *Ctx) int { return c.cmdRun([]string{"safe", "echo", "ok"}) }},
		{"peek", func(c *Ctx) int { return c.cmdPeek([]string{"safe"}) }},
		{"wait", func(c *Ctx) int { return c.cmdWait([]string{"safe", "--timeout", "1ms"}) }},
		{"send", func(c *Ctx) int { return c.cmdSend([]string{"safe", "--enter"}) }},
		{"log", func(c *Ctx) int { return c.cmdLog([]string{"safe"}) }},
		{"ls", func(c *Ctx) int { return c.cmdLs(nil) }},
		{"kill", func(c *Ctx) int { return c.cmdKill([]string{"--all"}) }},
		{"attach", func(c *Ctx) int { return c.cmdAttach([]string{"safe"}) }},
		{"watch", func(c *Ctx) int { return c.cmdWatch(nil) }},
		{"note", func(c *Ctx) int { return c.cmdNote([]string{"safe", "hello"}) }},
		{"doctor", func(c *Ctx) int { return c.cmdDoctor(nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			root := t.TempDir()
			c := newTestCtx(&buf, true)
			c.Tmux = tmux.New("../escape")
			c.StateDir = root
			if rc := tt.run(c); rc != 1 {
				t.Fatalf("rc = %d, want 1; output=%s", rc, buf.String())
			}
			env := decode(t, &buf)
			if env.Error == nil || env.Error.Code != output.CodeBadArgs || !strings.Contains(env.Error.Message, "invalid socket") {
				t.Fatalf("envelope = %+v, want invalid-socket E_BAD_ARGS", env)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("invalid socket touched state: %v", entries)
			}
		})
	}
}

func TestTerminalIdleAfterQuietRequiresCompletedPendingCommand(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "term"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.RawPath(), []byte("sleeping quietly\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendEvent(core.Event{Type: core.EvCmdStart, CmdID: 1, Offset: 0, Text: "sleep 60"}); err != nil {
		t.Fatal(err)
	}
	backdate := func() {
		old := time.Now().Add(-time.Minute)
		if err := os.Chtimes(j.RawPath(), old, old); err != nil {
			t.Fatal(err)
		}
	}
	backdate()
	if status, _, terminal := terminalStatusAfterQuiet(j, true, core.ModeHooks, time.Second); terminal || status != core.StatusRunning {
		t.Fatal("quiet pending command reported idle without a completion mark")
	}

	f, err := os.OpenFile(j.RawPath(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\x1b]133;D;0\x07"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	backdate()
	if status, _, terminal := terminalStatusAfterQuiet(j, true, core.ModeHooks, time.Second); !terminal || status != core.StatusIdle {
		t.Fatal("completed pending command should be idle after output quiesces")
	}
}

func TestTerminalStatusAfterQuietSurfacesPromptAndDeath(t *testing.T) {
	newJournal := func(t *testing.T, raw string) *journal.Journal {
		t.Helper()
		j, err := journal.Open(filepath.Join(t.TempDir(), "term"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(j.RawPath(), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := j.AppendEvent(core.Event{Type: core.EvCmdStart, CmdID: 1, Offset: 0, Text: "read answer"}); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Minute)
		if err := os.Chtimes(j.RawPath(), old, old); err != nil {
			t.Fatal(err)
		}
		return j
	}

	j := newJournal(t, "Continue [y/N]? ")
	if status, prompt, terminal := terminalStatusAfterQuiet(j, true, core.ModeHooks, time.Second); !terminal || status != core.StatusAwaitingInput || !strings.Contains(prompt, "[y/N]") {
		t.Fatalf("prompt status = (%q, %q, %v), want awaiting-input", status, prompt, terminal)
	}

	j = newJournal(t, "quiet\n")
	if status, _, terminal := terminalStatusAfterQuiet(j, false, core.ModeHooks, time.Second); !terminal || status != core.StatusDead {
		t.Fatalf("dead status = (%q, %v), want dead", status, terminal)
	}
}
