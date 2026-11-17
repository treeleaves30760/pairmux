package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

func mkLayerTerm(t *testing.T, root, socket, name string) {
	t.Helper()
	dir := filepath.Join(state.SocketDirFor(root, mux.BackendTmux, socket), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&journal.Journal{Dir: dir}).WriteMeta(core.Meta{Name: name, Socket: socket}); err != nil {
		t.Fatal(err)
	}
}

// scopedCtx is a ctx on socket for a process running inside terminal name of
// layer owner, i.e. with the scope pairmux gives that terminal.
func scopedCtx(t *testing.T, root, socket, owner, name string) (*Ctx, *bytes.Buffer) {
	t.Helper()
	t.Setenv(ScopeEnv, state.ChildSocket(owner, name))
	t.Setenv(OwnerSocketEnv, owner)
	t.Setenv(NameEnv, name)
	var buf bytes.Buffer
	return &Ctx{Mux: tmux.New(socket), JSON: true, StateDir: root, Stdout: &buf}, &buf
}

func TestEnforceScope(t *testing.T) {
	root := t.TempDir()
	mkLayerTerm(t, root, "top", "boss")
	mkLayerTerm(t, root, "top", "peer")
	boss := state.ChildSocket("top", "boss")
	mkLayerTerm(t, root, boss, "worker")
	mkLayerTerm(t, root, boss, "sibling")
	worker := state.ChildSocket(boss, "worker")
	mkLayerTerm(t, root, worker, "sub")
	sub := state.ChildSocket(worker, "sub")

	cases := []struct {
		desc   string
		socket string
		args   []string
		allow  bool
	}{
		{"own layer", worker, []string{"ls"}, true},
		{"layer below", sub, []string{"peek", "x"}, true},
		{"owner layer", boss, []string{"ls"}, false},
		{"the top", "top", []string{"send", "boss", "--text", "x"}, false},
		{"sibling's layer", state.ChildSocket(boss, "sibling"), []string{"ls"}, false},
		{"kill upward", boss, []string{"kill", "worker"}, false},
		{"note on itself in the owner layer", boss, []string{"note", "worker", "done"}, true},
		{"note on a sibling in the owner layer", boss, []string{"note", "sibling", "hi"}, false},
		{"no-terminal command anywhere", "top", []string{"version"}, true},
		{"report is exempt", "top", []string{"report", "done"}, true},
		{"after --", "top", []string{"--", "ls"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			c, buf := scopedCtx(t, root, tc.socket, boss, "worker")
			rc, refused := c.enforceScope(tc.args)
			if refused == tc.allow {
				t.Fatalf("refused=%v, want allow=%v (%s)", refused, tc.allow, buf.String())
			}
			if refused {
				e := decode(t, buf)
				if rc != 1 || e.Error == nil || e.Error.Code != output.CodeScope {
					t.Fatalf("rc=%d envelope=%+v, want E_SCOPE", rc, e)
				}
			}
		})
	}
}

func TestEnforceScopeIgnoresUnscopedCallers(t *testing.T) {
	t.Setenv(ScopeEnv, "")
	var buf bytes.Buffer
	c := &Ctx{Mux: tmux.New("anything"), JSON: true, StateDir: t.TempDir(), Stdout: &buf}
	if _, refused := c.enforceScope([]string{"kill", "--all"}); refused {
		t.Fatalf("a human (no scope) was refused: %s", buf.String())
	}
}

func TestResolveTargetDescendsAndRewritesReplies(t *testing.T) {
	var buf bytes.Buffer
	c := &Ctx{Mux: tmux.New("top"), JSON: true, StateDir: t.TempDir(), Stdout: &buf}
	name := "boss/worker/sub"
	if rc, bad := c.resolveTarget(&name); bad {
		t.Fatalf("rc=%d %s", rc, buf.String())
	}
	want := state.ChildSocket(state.ChildSocket("top", "boss"), "worker")
	if name != "sub" || c.Mux.SocketName() != want {
		t.Fatalf("leaf=%q socket=%q, want sub on %q", name, c.Mux.SocketName(), want)
	}
	c.emit(output.Envelope{Status: "idle", Terminal: "sub", Next: []string{
		`pairmux run sub "echo hello"`, "pairmux wait sub,other --done", "pairmux ls", "plain prose about sub",
	}})
	e := decode(t, &buf)
	if e.Terminal != "boss/worker/sub" {
		t.Fatalf("terminal=%q", e.Terminal)
	}
	got := strings.Join(e.Next, "|")
	wantNext := `pairmux run boss/worker/sub "echo hello"|pairmux wait boss/worker/sub,other --done|pairmux ls|plain prose about sub`
	if got != wantNext {
		t.Fatalf("next=%q\nwant %q", got, wantNext)
	}
}

func TestResolveTargetRejectsBadPaths(t *testing.T) {
	for _, ref := range []string{"boss/../x", "a//b", "/tmp/x", "boss/", "Boss/x", "boss/x y"} {
		var buf bytes.Buffer
		c := &Ctx{Mux: tmux.New("top"), JSON: true, StateDir: t.TempDir(), Stdout: &buf}
		name := ref
		if _, bad := c.resolveTarget(&name); !bad {
			t.Fatalf("%q accepted as %q", ref, name)
		}
	}
}

func TestResolveTargetRefusesMixedLayers(t *testing.T) {
	var buf bytes.Buffer
	c := &Ctx{Mux: tmux.New("top"), JSON: true, StateDir: t.TempDir(), Stdout: &buf}
	a, b := "boss/x", "peer/y"
	if _, bad := c.resolveTarget(&a); bad {
		t.Fatal(buf.String())
	}
	if _, bad := c.resolveTarget(&b); !bad {
		t.Fatal("terminals of two layers accepted in one call")
	}
}

func TestReportOutsideATerminal(t *testing.T) {
	t.Setenv(OwnerSocketEnv, "")
	t.Setenv(NameEnv, "")
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdReport([]string{"done"}); rc != 1 {
		t.Fatalf("rc=%d", rc)
	}
	if e := decode(t, &buf); e.Error == nil || e.Error.Code != output.CodeBadArgs {
		t.Fatalf("envelope=%+v", e)
	}
}
