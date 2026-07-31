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
	"github.com/treeleaves30760/pairmux/internal/state"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

func TestParseOlderThan(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"36h", 36 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"", 0, true},
		{"d", 0, true},
		{"7dd", 0, true},
		{"soon", 0, true},
	}
	for _, tt := range tests {
		got, err := parseOlderThan(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseOlderThan(%q) = %v, %v; want %v, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{2048, "2KB"},
		{5 << 20, "5MB"},
		{3 << 30, "3.0GB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.n); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

// prTestSocket never has a server, so every state-dir terminal resolves dead.
const prTestSocket = "pmx-unit-nonexistent"

// newPruneCtx returns a Ctx over a temp state root and the namespace dir that
// prune will sweep for its socket.
func newPruneCtx(t *testing.T, buf *bytes.Buffer) (*Ctx, string) {
	t.Helper()
	root := t.TempDir()
	c := &Ctx{Tmux: tmux.New(prTestSocket), JSON: true, StateDir: root, Stdout: buf}
	return c, state.SocketDir(root, prTestSocket)
}

// mkDeadTerminal materializes a terminal state dir with matching meta so
// ListAt/ResolveAt see it (dead: the socket has no server, hence no pane).
func mkDeadTerminal(t *testing.T, ns, name string, rawBytes int) string {
	t.Helper()
	dir := filepath.Join(ns, name)
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.WriteMeta(core.Meta{Name: name, Socket: prTestSocket, Mode: core.ModeHooks}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.RawPath(), bytes.Repeat([]byte("x"), rawBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPruneSweepRemovesDeadAndArchives(t *testing.T) {
	var buf bytes.Buffer
	c, ns := newPruneCtx(t, &buf)
	dead := mkDeadTerminal(t, ns, "dead1", 100)
	if err := os.Rename(mkDeadTerminal(t, ns, "rotated", 50), filepath.Join(ns, "orphan.prev")); err != nil {
		t.Fatal(err)
	}

	// Dry run keeps everything.
	if rc := c.cmdPrune([]string{"--dry-run"}); rc != 0 {
		t.Fatalf("dry-run rc = %d, output %s", rc, buf.String())
	}
	e := decode(t, &buf)
	if e.Status != "ok" || !strings.Contains(e.Output, "would prune") {
		t.Fatalf("dry-run envelope = %+v", e)
	}
	if !isDir(dead) || !isDir(filepath.Join(ns, "orphan.prev")) {
		t.Fatal("dry-run removed directories")
	}

	// Real prune removes the dead terminal and the orphaned archive.
	buf.Reset()
	if rc := c.cmdPrune(nil); rc != 0 {
		t.Fatalf("prune rc = %d, output %s", rc, buf.String())
	}
	e = decode(t, &buf)
	if e.Status != "pruned" {
		t.Fatalf("envelope = %+v", e)
	}
	if isDir(dead) || isDir(filepath.Join(ns, "orphan.prev")) {
		t.Fatal("prune left directories behind")
	}
}

func TestPruneOlderThanKeepsFresh(t *testing.T) {
	var buf bytes.Buffer
	c, ns := newPruneCtx(t, &buf)
	fresh := mkDeadTerminal(t, ns, "fresh", 10)

	if rc := c.cmdPrune([]string{"--older-than", "7d"}); rc != 0 {
		t.Fatalf("rc = %d, output %s", rc, buf.String())
	}
	e := decode(t, &buf)
	if !isDir(fresh) {
		t.Fatal("prune removed a journal newer than --older-than")
	}
	if !strings.Contains(e.Output, "newer than --older-than") {
		t.Fatalf("output = %q, want kept reason", e.Output)
	}
}

func TestPruneSkipsHeldLock(t *testing.T) {
	var buf bytes.Buffer
	c, ns := newPruneCtx(t, &buf)
	dir := mkDeadTerminal(t, ns, "locked", 10)
	release, err := (&journal.Journal{Dir: dir}).AcquireWriteLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if rc := c.cmdPrune(nil); rc != 0 {
		t.Fatalf("rc = %d, output %s", rc, buf.String())
	}
	e := decode(t, &buf)
	if !isDir(dir) {
		t.Fatal("prune removed a directory whose write lock is held")
	}
	if !strings.Contains(e.Output, "write lock held") {
		t.Fatalf("output = %q, want lock-held reason", e.Output)
	}
}

func TestPruneByNameDeadAndMissing(t *testing.T) {
	var buf bytes.Buffer
	c, ns := newPruneCtx(t, &buf)
	dir := mkDeadTerminal(t, ns, "gone", 10)

	if rc := c.cmdPrune([]string{"gone"}); rc != 0 {
		t.Fatalf("rc = %d, output %s", rc, buf.String())
	}
	if isDir(dir) {
		t.Fatal("named prune left the dead terminal")
	}

	buf.Reset()
	if rc := c.cmdPrune([]string{"gone"}); rc != 1 {
		t.Fatalf("rc = %d, want 1 for missing terminal", rc)
	}
	e := decode(t, &buf)
	if e.OK || e.Error == nil || e.Error.Code != output.CodeNoTerminal {
		t.Fatalf("envelope = %+v, want E_NO_TERMINAL", e)
	}
}

func TestPruneNothing(t *testing.T) {
	var buf bytes.Buffer
	c, _ := newPruneCtx(t, &buf)
	if rc := c.cmdPrune(nil); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	e := decode(t, &buf)
	if e.Status != "ok" || e.Output != "nothing to prune" {
		t.Fatalf("envelope = %+v", e)
	}
}
