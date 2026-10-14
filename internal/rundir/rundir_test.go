package rundir

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPathStaysWithinLimit(t *testing.T) {
	d := "/tmp/pairmux-501"
	short, err := socketPathIn(d, "pairmux")
	if err != nil || short != filepath.Join(d, "pairmux.sock") {
		t.Fatalf("short = %q, %v", short, err)
	}
	long := strings.Repeat("a", 64)
	p, err := socketPathIn(filepath.Join(d, strings.Repeat("d", 30)), long)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) > maxSocketPath || !strings.Contains(p, "s-") {
		t.Fatalf("long name path %q (%d bytes)", p, len(p))
	}
	again, _ := socketPathIn(filepath.Join(d, strings.Repeat("d", 30)), long)
	if again != p {
		t.Fatal("digest path is not stable")
	}
	if _, err := socketPathIn("/"+strings.Repeat("x", 120), "pairmux"); err == nil {
		t.Fatal("expected an error for a runtime dir too deep for any socket")
	}
}

func TestEnvironmentMovesTheRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PAIRMUX_TMPDIR", root)
	d, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d, canonical(root)) {
		t.Fatalf("Ensure() = %q, want under %q", d, root)
	}
	if !strings.HasPrefix(Identity("x"), "builtin:"+d) {
		t.Fatalf("Identity = %q", Identity("x"))
	}
}
