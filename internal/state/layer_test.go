package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/mux"
)

// mkTerm writes the meta.json that makes name a terminal of socket's layer.
func mkTerm(t *testing.T, root, socket, name string) {
	t.Helper()
	dir := filepath.Join(SocketDirFor(root, mux.BackendTmux, socket), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&journal.Journal{Dir: dir}).WriteMeta(core.Meta{Name: name, Socket: socket}); err != nil {
		t.Fatal(err)
	}
}

func TestIsDescendantLayer(t *testing.T) {
	root := t.TempDir()
	// top: boss, other; boss's layer: worker; worker's layer: sub
	mkTerm(t, root, "top", "boss")
	mkTerm(t, root, "top", "other")
	boss := ChildSocket("top", "boss")
	mkTerm(t, root, boss, "worker")
	worker := ChildSocket(boss, "worker")
	mkTerm(t, root, worker, "sub")
	sub := ChildSocket(worker, "sub")

	cases := []struct {
		anc, target string
		want        bool
	}{
		{"top", "top", true},
		{"top", boss, true},
		{"top", worker, true},
		{"top", sub, true}, // a layer with no terminals yet, one level down
		{boss, worker, true},
		{boss, sub, true},
		{worker, boss, false},                      // upward
		{worker, "top", false},                     // to the top
		{boss, ChildSocket("top", "other"), false}, // sideways
		{worker, ChildSocket(boss, "nobody"), false},
		{boss, "top-boss-worker-zzz", false},
	}
	for _, c := range cases {
		if got := IsDescendantLayer(root, mux.BackendTmux, c.anc, c.target); got != c.want {
			t.Errorf("IsDescendantLayer(%q, %q) = %v, want %v", c.anc, c.target, got, c.want)
		}
	}
}

func TestIsDescendantLayerFollowsHashedNames(t *testing.T) {
	root := t.TempDir()
	long := "a234567890123456789012345678901"
	parent := "p" + long // a socket long enough that children are digest-named
	mkTerm(t, root, parent, long)
	child := ChildSocket(parent, long)
	mkTerm(t, root, child, long)
	grand := ChildSocket(child, long)
	if !IsDescendantLayer(root, mux.BackendTmux, parent, grand) {
		t.Fatalf("hashed grandchild %q not recognised under %q", grand, parent)
	}
	if IsDescendantLayer(root, mux.BackendTmux, grand, parent) {
		t.Fatal("ancestor recognised as descendant")
	}
}
