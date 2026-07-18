package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

func writeMetaAt(t *testing.T, dir, name, socket string) {
	t.Helper()
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.WriteMeta(core.Meta{Name: name, PaneID: "%" + name, Mode: core.ModeHooks, Socket: socket}); err != nil {
		t.Fatal(err)
	}
}

func TestSocketDirIsolationAndConfinement(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMUX_TMPDIR", "")
	defaultDir := SocketDir(root, "")
	if got := SocketDir(root, core.DefaultSocket); got != defaultDir {
		t.Fatalf("empty/default socket dirs differ: %q vs %q", defaultDir, got)
	}
	if defaultDir == root || filepath.Dir(defaultDir) != filepath.Join(root, ".sockets") {
		t.Fatalf("default endpoint is not namespaced: %q", defaultDir)
	}

	a := SocketDir(root, "build-a")
	b := SocketDir(root, "build-b")
	if a == b || a == root || b == root {
		t.Fatalf("custom namespaces not isolated: a=%q b=%q root=%q", a, b, root)
	}
	for _, socket := range []string{"../escape", "/absolute/socket", strings.Repeat("x", 4096)} {
		dir := SocketDir(root, socket)
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("SocketDir(%q) escaped root: dir=%q rel=%q err=%v", socket, dir, rel, err)
		}
		if filepath.Dir(dir) != filepath.Join(root, ".sockets") {
			t.Fatalf("SocketDir(%q) = %q, want hashed .sockets child", socket, dir)
		}
	}

	tmpA := t.TempDir()
	tmpB := t.TempDir()
	t.Setenv("TMUX_TMPDIR", tmpA)
	a = SocketDir(root, "same-name")
	t.Setenv("TMUX_TMPDIR", tmpB)
	b = SocketDir(root, "same-name")
	if a == b {
		t.Fatalf("same -L name under different TMUX_TMPDIR roots collided: %q", a)
	}
}

func TestSocketScopedStateAndLegacyFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMUX_TMPDIR", "")
	writeMetaAt(t, filepath.Join(root, "default-old"), "default-old", core.DefaultSocket)
	writeMetaAt(t, filepath.Join(SocketDir(root, "sock-a"), "only-a"), "only-a", "sock-a")
	writeMetaAt(t, filepath.Join(SocketDir(root, "sock-b"), "only-b"), "only-b", "sock-b")
	// Pre-namespace custom state lacks TMUX_TMPDIR/uid identity. It remains
	// untouched but is not claimed by any new endpoint namespace.
	legacyCustom := filepath.Join(root, "custom-old")
	writeMetaAt(t, legacyCustom, "custom-old", "sock-a")

	names := func(socket string) []string {
		terms, err := listAt(fakeLister{}, root, socket)
		if err != nil {
			t.Fatalf("listAt(%q): %v", socket, err)
		}
		out := make([]string, 0, len(terms))
		for _, term := range terms {
			out = append(out, term.Name)
		}
		return out
	}
	if got, want := names(core.DefaultSocket), []string{"default-old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("default state = %v, want %v", got, want)
	}
	if got, want := names("sock-a"), []string{"only-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sock-a state = %v, want %v", got, want)
	}
	if got, want := names("sock-b"), []string{"only-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sock-b state = %v, want %v", got, want)
	}
	if _, err := os.Stat(legacyCustom); err != nil {
		t.Fatalf("legacy custom state was moved or removed: %v", err)
	}
	if _, err := resolveAt(fakeLister{}, root, "sock-a", "custom-old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("custom legacy resolve err = %v, want ErrNotFound", err)
	}
}

type countingLister struct{ calls int }

func (f *countingLister) ListManaged() ([]tmux.PaneInfo, error) {
	f.calls++
	return nil, nil
}

func TestResolveRejectsInvalidNameBeforeTmuxOrState(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", "/tmp/absolute", "a/b", ".."} {
		f := &countingLister{}
		if _, err := resolveAt(f, root, core.DefaultSocket, name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("resolveAt(%q) err = %v, want ErrInvalidName", name, err)
		}
		if f.calls != 0 {
			t.Fatalf("resolveAt(%q) contacted tmux %d times", name, f.calls)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid resolves touched state root: %v", entries)
	}
}
