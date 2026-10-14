package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/muxd"
	"github.com/treeleaves30760/pairmux/internal/output"
)

// fakeBuiltin is a built-in backend with a fixed pane list; only what attach
// touches does anything.
type fakeBuiltin struct{ panes []mux.PaneInfo }

func (f *fakeBuiltin) Backend() string                            { return mux.BackendBuiltin }
func (f *fakeBuiltin) SocketName() string                         { return "pmx-fake" }
func (f *fakeBuiltin) Version() (string, error)                   { return "test", nil }
func (f *fakeBuiltin) AtomicJournal() bool                        { return true }
func (f *fakeBuiltin) EnsureSession() error                       { return nil }
func (f *fakeBuiltin) NewWindow(mux.NewWindowReq) (string, error) { return "", nil }
func (f *fakeBuiltin) PipePaneAppend(string, string) error        { return nil }
func (f *fakeBuiltin) SendLiteral(string, string) error           { return nil }
func (f *fakeBuiltin) SendKeys(string, ...string) error           { return nil }
func (f *fakeBuiltin) CapturePane(string, int) (string, error)    { return "", nil }
func (f *fakeBuiltin) SetPaneOption(string, string, string) error { return nil }
func (f *fakeBuiltin) ListManaged() ([]mux.PaneInfo, error)       { return f.panes, nil }
func (f *fakeBuiltin) KillWindowOf(string) error                  { return nil }
func (f *fakeBuiltin) SelectWindow(string) error                  { return nil }
func (f *fakeBuiltin) HasSession() bool                           { return true }
func (f *fakeBuiltin) KillServer() error                          { return nil }
func (f *fakeBuiltin) PaneTTY(string) (string, error)             { return "", nil }
func (f *fakeBuiltin) PaneAudience(string) (int, bool, error)     { return 0, false, nil }

// withBuiltinAttach records attach calls instead of taking over a terminal.
func withBuiltinAttach(t *testing.T, self string, res muxd.AttachResult) *[]string {
	t.Helper()
	var calls []string
	oldAttach, oldSelf := builtinAttach, insideBuiltinPane
	t.Cleanup(func() { builtinAttach, insideBuiltinPane = oldAttach, oldSelf })
	builtinAttach = func(socket, pane string) (muxd.AttachResult, error) {
		calls = append(calls, socket+"/"+pane)
		return res, nil
	}
	insideBuiltinPane = func() string { return self }
	return &calls
}

func builtinCtx(t *testing.T, buf *bytes.Buffer, names ...string) *Ctx {
	f := &fakeBuiltin{}
	for i, n := range names {
		f.panes = append(f.panes, mux.PaneInfo{PaneID: "%" + string(rune('0'+i)), Window: n, Name: n})
	}
	return &Ctx{Mux: f, JSON: true, StateDir: t.TempDir(), Stdout: buf}
}

func TestBuiltinAttachWorksInsideTmuxAndReportsDetach(t *testing.T) {
	withTTY(t, true, true) // nested in tmux: only the tmux backend refuses that
	calls := withBuiltinAttach(t, "", muxd.AttachResult{Detached: true})
	var buf bytes.Buffer
	c := builtinCtx(t, &buf, "build")
	if rc := c.cmdAttach([]string{"build"}); rc != 0 {
		t.Fatalf("rc = %d: %s", rc, buf.String())
	}
	if len(*calls) != 1 || (*calls)[0] != "pmx-fake/%0" {
		t.Fatalf("attach calls = %v", *calls)
	}
	if !strings.Contains(buf.String(), "[detached from build]") || !strings.Contains(buf.String(), "pairmux note build") {
		t.Fatalf("detach message = %q", buf.String())
	}
}

func TestBuiltinAttachRefusesItsOwnPane(t *testing.T) {
	withTTY(t, true, false)
	calls := withBuiltinAttach(t, "pmx-fake/%0", muxd.AttachResult{})
	var buf bytes.Buffer
	c := builtinCtx(t, &buf, "build")
	if rc := c.cmdAttach([]string{"build"}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	e := decode(t, &buf)
	if e.Error == nil || e.Error.Code != output.CodeBadArgs || !strings.Contains(e.Error.Message, "itself") || len(*calls) != 0 {
		t.Fatalf("envelope = %+v, calls = %v", e, *calls)
	}
}

func TestBuiltinAttachWithoutNamePicksTheOnlyTerminal(t *testing.T) {
	withTTY(t, true, false)
	calls := withBuiltinAttach(t, "", muxd.AttachResult{Message: "terminal solo exited with status 0"})
	var buf bytes.Buffer
	c := builtinCtx(t, &buf, "solo")
	if rc := c.cmdAttach(nil); rc != 0 {
		t.Fatalf("rc = %d: %s", rc, buf.String())
	}
	if len(*calls) != 1 || (*calls)[0] != "pmx-fake/%0" || !strings.Contains(buf.String(), "[terminal solo exited") {
		t.Fatalf("calls = %v, out = %q", *calls, buf.String())
	}
}

func TestBuiltinAttachWithoutNameListsSeveral(t *testing.T) {
	withTTY(t, true, false)
	calls := withBuiltinAttach(t, "", muxd.AttachResult{})
	var buf bytes.Buffer
	c := builtinCtx(t, &buf, "api", "web")
	if rc := c.cmdAttach(nil); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	e := decode(t, &buf)
	if e.Error == nil || e.Error.Code != output.CodeBadArgs || !strings.Contains(e.Error.Message, "api, web") || len(*calls) != 0 {
		t.Fatalf("envelope = %+v", e)
	}
}
