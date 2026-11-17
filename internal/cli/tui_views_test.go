package cli

import (
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/backend"
	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/state"
)

func TestSplitCommandLine(t *testing.T) {
	cases := map[string][]string{
		`log api --cmd 2`:       {"log", "api", "--cmd", "2"},
		`run {} "make test"`:    {"run", "{}", "make test"},
		`send x --text 'it''s'`: {"send", "x", "--text", "its"},
		`note x "say \"hi\""`:   {"note", "x", `say "hi"`},
		`note x a\ b  ''`:       {"note", "x", "a b", ""},
		`  doctor  `:            {"doctor"},
	}
	for in, want := range cases {
		got, err := splitCommandLine(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("split(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := splitCommandLine(`note x "open`); err == nil {
		t.Error("unclosed quote accepted")
	}
}

func TestHardWrapKeepsEveryCell(t *testing.T) {
	got := hardWrap("abcdefg", 3)
	if !reflect.DeepEqual(got, []string{"abc", "def", "g"}) {
		t.Fatalf("wrap = %q", got)
	}
	if got := hardWrap("中文字", 4); !reflect.DeepEqual(got, []string{"中文", "字"}) {
		t.Fatalf("wide wrap = %q", got)
	}
	if got := hardWrap("", 4); !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("empty wrap = %q", got)
	}
}

func TestHumanAtTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the TUI is unix-only, so ls never opens it on Windows")
	}
	oldTTY, oldStdin := isTTY, stdinIsTTY
	t.Cleanup(func() { isTTY, stdinIsTTY = oldTTY, oldStdin })
	isTTY = func() bool { return true }
	stdinIsTTY = func() bool { return true }
	for _, k := range append([]string{NoTUIEnv, ScopeEnv, NameEnv}, agentShellEnv...) {
		t.Setenv(k, "")
	}
	t.Setenv("TERM", "xterm-256color")
	c := &Ctx{Stdout: os.Stdout}
	if !c.humanAtTerminal() {
		t.Fatal("a person at a terminal was not recognised")
	}
	for _, k := range []string{ScopeEnv, NameEnv, "CLAUDECODE", "CODEX_SANDBOX", NoTUIEnv} {
		t.Setenv(k, "1")
		if c.humanAtTerminal() {
			t.Errorf("%s set: still treated as a person", k)
		}
		t.Setenv(k, "")
	}
	if (&Ctx{Stdout: os.Stdout, JSON: true}).humanAtTerminal() {
		t.Error("--json treated as a person")
	}
	if (&Ctx{Stdout: &strings.Builder{}}).humanAtTerminal() {
		t.Error("captured stdout treated as a person")
	}
	stdinIsTTY = func() bool { return false }
	if c.humanAtTerminal() {
		t.Error("piped stdin treated as a person")
	}
}

func TestActivityLinesTellTheStory(t *testing.T) {
	dir := t.TempDir()
	j := &journal.Journal{Dir: dir}
	t0 := time.Now().Add(-time.Minute)
	zero, one := 0, 1
	for _, ev := range []core.Event{
		{TS: t0, Type: core.EvCreated, Text: "api"},
		{TS: t0.Add(time.Second), Type: core.EvCmdStart, CmdID: 1, Text: "go test ./..."},
		{TS: t0.Add(3 * time.Second), Type: core.EvCmdEnd, CmdID: 1, ExitCode: &one},
		{TS: t0.Add(4 * time.Second), Type: core.EvCmdStart, CmdID: 2, Text: "make"},
		{TS: t0.Add(5 * time.Second), Type: core.EvCmdEnd, CmdID: 2, ExitCode: &zero},
		{TS: t0.Add(6 * time.Second), Type: core.EvNote, Text: "fixed the token"},
	} {
		if err := j.AppendEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	n := treeNode{Path: "lead/api", Socket: "pairmux-lead", Status: core.StatusIdle,
		Term: state.Terminal{Name: "api", Dir: dir}}
	text := strings.Join(activityLines(n), "\n")
	for _, want := range []string{"path      lead/api", "$ go test ./...   #1", "#1 exit 1 ✕ after 2s",
		"#2 exit 0 ✓", "✉ note: fixed the token   (not seen by its agent yet)"} {
		if !strings.Contains(text, want) {
			t.Errorf("activity lacks %q:\n%s", want, text)
		}
	}
}

func TestCommandLineRunsInTheBackground(t *testing.T) {
	c := &Ctx{Mux: backend.OpenKind(mux.BackendBuiltin, "pmx-tui-test"), StateDir: t.TempDir()}
	a := &tuiApp{c: c, collapsed: map[string]bool{}, results: make(chan tuiResult, 1)}
	if msg := a.runCommand("attach api", "api"); !strings.Contains(msg, "⏎") {
		t.Fatalf("attach not refused with a pointer to ⏎: %q", msg)
	}
	if msg := a.runCommand("log {}", ""); msg == "" {
		t.Fatal("{} with nothing selected was accepted")
	}
	if msg := a.runCommand("pairmux version", ""); msg != "" {
		t.Fatalf("version refused: %s", msg)
	}
	if a.mode != modeView || a.viewer == nil || !a.viewer.busy || !a.viewer.reloadable {
		t.Fatalf("reader not opened busy: mode=%v viewer=%+v", a.mode, a.viewer)
	}
	select {
	case r := <-a.results:
		a.applyResult(r)
	case <-time.After(5 * time.Second):
		t.Fatal("version never finished")
	}
	if a.viewer.busy || a.viewer.status != "exit 0" || strings.TrimSpace(strings.Join(a.viewer.lines, "\n")) == "" {
		t.Fatalf("reader after version: %+v", a.viewer)
	}
	if a.lastCmd != "pairmux version" {
		t.Fatalf("lastCmd = %q", a.lastCmd)
	}
}
