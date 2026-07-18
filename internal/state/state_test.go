package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

func TestValidName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"a", true},
		{"t1", true},
		{"build-01", true},
		{"a_b-c", true},
		{"0abc", true},
		{"", false},
		{"-lead", false},
		{"_lead", false},
		{"UpperCase", false},
		{"has space", false},
		{"has.dot", false},
		{"demo.prev", false},
		{"way-too-long-name-that-keeps-going-past-limit", false},
	}
	for _, tt := range tests {
		if got := ValidName(tt.name); got != tt.ok {
			t.Errorf("ValidName(%q) = %v, want %v", tt.name, got, tt.ok)
		}
	}
}

func TestNextAutoName(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		want     string
	}{
		{"empty", nil, "t1"},
		{"one", []string{"t1"}, "t2"},
		{"gap not filled", []string{"t1", "t3"}, "t4"},
		{"ignores non-auto", []string{"build", "t2", "deploy"}, "t3"},
		{"ignores malformed", []string{"tx", "t1a", "t02x"}, "t1"},
		{"mixed", []string{"t10", "t2"}, "t11"},
	}
	for _, tt := range tests {
		if got := NextAutoName(tt.existing); got != tt.want {
			t.Errorf("%s: NextAutoName(%v) = %q, want %q", tt.name, tt.existing, got, tt.want)
		}
	}
}

func TestIsNoServer(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("tmux -L x list-panes: exit status 1: no server running on /tmp/x"), true},
		{errors.New("error connecting to /tmp/tmux-501/x (No such file or directory)"), true},
		{errors.New("error connecting to /tmp/tmux-501/x (Connection refused)"), true},
		{errors.New("error connecting to /tmp/tmux-501/x (Permission denied)"), false},
		{errors.New("error connecting to /deep/path/tmux-501/x (File name too long)"), false},
		{errors.New("error connecting to /tmp/tmux-501/x (unexpected failure)"), false},
		{errors.New("some other tmux failure"), false},
	}
	for _, tt := range tests {
		if got := IsNoServer(tt.err); got != tt.want {
			t.Errorf("IsNoServer(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

// fakeLister is an injectable ListManaged for List/Resolve tests.
type fakeLister struct {
	panes []tmux.PaneInfo
	err   error
}

func (f fakeLister) ListManaged() ([]tmux.PaneInfo, error) { return f.panes, f.err }

// writeTerm creates a terminal state dir with a meta.json so state discovers it.
func writeTerm(t *testing.T, name string) {
	t.Helper()
	j, err := journal.Open(Dir(name))
	if err != nil {
		t.Fatalf("open journal for %s: %v", name, err)
	}
	if err := j.WriteMeta(core.Meta{Name: name, Mode: core.ModeHooks, PaneID: "%meta-" + name}); err != nil {
		t.Fatalf("write meta for %s: %v", name, err)
	}
}

func TestListUnion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PAIRMUX_STATE_DIR", dir)

	// On-disk terminals.
	writeTerm(t, "alive1")
	writeTerm(t, "deadonly")
	// A shim directory (no meta.json) and an archived dir must be ignored.
	if err := os.MkdirAll(filepath.Join(dir, "shim-zsh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shim-zsh", ".zshrc"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTerm(t, "deadonly") // ensure exists
	if err := os.MkdirAll(filepath.Join(dir, "alive1.prev"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alive1.prev", "meta.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := fakeLister{panes: []tmux.PaneInfo{
		{PaneID: "%1", Name: "alive1", Dead: false},
		{PaneID: "%2", Name: "paneonly", Dead: false},
		{PaneID: "%3", Name: "deadonly", Dead: true}, // dead pane over the dir
	}}

	terms, err := list(fake)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]Terminal{}
	var names []string
	for _, tm := range terms {
		got[tm.Name] = tm
		names = append(names, tm.Name)
	}
	want := []string{"alive1", "deadonly", "paneonly"} // sorted, shim/.prev excluded
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if !got["alive1"].Alive {
		t.Errorf("alive1 should be alive")
	}
	if got["deadonly"].Alive {
		t.Errorf("deadonly (dead pane) should not be alive")
	}
	if got["alive1"].PaneID != "%1" {
		t.Errorf("alive1 PaneID = %q, want %%1", got["alive1"].PaneID)
	}
	if got["paneonly"].PaneID != "%2" || !got["paneonly"].Alive {
		t.Errorf("paneonly = %+v", got["paneonly"])
	}
}

func TestListNoServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PAIRMUX_STATE_DIR", dir)
	writeTerm(t, "onlydir")

	fake := fakeLister{err: errors.New("no server running on /tmp/x")}
	terms, err := list(fake)
	if err != nil {
		t.Fatalf("list with no-server should succeed: %v", err)
	}
	if len(terms) != 1 || terms[0].Name != "onlydir" || terms[0].Alive {
		t.Fatalf("terms = %+v, want single dead onlydir", terms)
	}
}

func TestListPropagatesRealError(t *testing.T) {
	t.Setenv("PAIRMUX_STATE_DIR", t.TempDir())
	fake := fakeLister{err: errors.New("tmux exploded")}
	if _, err := list(fake); err == nil {
		t.Fatal("expected real tmux error to propagate")
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PAIRMUX_STATE_DIR", dir)
	writeTerm(t, "alive1")
	writeTerm(t, "deadonly")

	fake := fakeLister{panes: []tmux.PaneInfo{{PaneID: "%1", Name: "alive1"}}}

	tm, err := resolve(fake, "alive1")
	if err != nil {
		t.Fatalf("resolve alive1: %v", err)
	}
	if !tm.Alive || tm.PaneID != "%1" || tm.Mode != core.ModeHooks {
		t.Errorf("alive1 = %+v", tm)
	}

	tm, err = resolve(fake, "deadonly")
	if err != nil {
		t.Fatalf("resolve deadonly: %v", err)
	}
	if tm.Alive {
		t.Errorf("deadonly should be dead: %+v", tm)
	}

	if _, err := resolve(fake, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolve ghost err = %v, want ErrNotFound", err)
	}
}

func TestBaseDirPrecedence(t *testing.T) {
	t.Setenv("PAIRMUX_STATE_DIR", "/explicit/state")
	t.Setenv("XDG_STATE_HOME", "/xdg")
	if got := BaseDir(); got != "/explicit/state" {
		t.Errorf("BaseDir with PAIRMUX_STATE_DIR = %q", got)
	}

	t.Setenv("PAIRMUX_STATE_DIR", "")
	if got := BaseDir(); got != filepath.Join("/xdg", "pairmux") {
		t.Errorf("BaseDir with XDG_STATE_HOME = %q", got)
	}
}
