//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The exit trap keeps the owned bash writer alive until the test releases it.
// Its control files belong to the parent test, so premature TempDir removal in
// the child cannot release the trap or erase evidence of an early cleanup.
func TestCleanupWaitsForOwnedHistoryWriter(t *testing.T) {
	if !haveTmux || !isExec(bashShell) {
		t.Skip("tmux and bash required")
	}
	for _, killBeforeCleanup := range []bool{false, true} {
		t.Run(fmt.Sprintf("kill_before_cleanup=%t", killBeforeCleanup), func(t *testing.T) {
			testCleanupHistoryWriter(t, killBeforeCleanup)
		})
	}
}

func testCleanupHistoryWriter(t *testing.T, killBeforeCleanup bool) {
	t.Helper()
	control := t.TempDir()
	gate := filepath.Join(control, "release")
	ready := filepath.Join(control, "ready")
	finished := filepath.Join(control, "finished")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cleanupReturned := make(chan struct{})
	cleanupStarted := make(chan struct{})
	controllerDone := make(chan error, 1)
	go func() {
		if err := waitCleanupFile(ctx, ready); err != nil {
			controllerDone <- err
			return
		}
		select {
		case <-cleanupStarted:
		case <-ctx.Done():
			controllerDone <- ctx.Err()
			return
		}
		// Observe a forbidden early return while the writer is blocked. This is
		// not a teardown sleep: the FIFO controls exit, and read -t below also
		// bounds the writer if the test itself fails.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-cleanupReturned:
		case <-timer.C:
		case <-ctx.Done():
			controllerDone <- ctx.Err()
			return
		}
		f, err := os.OpenFile(gate, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_, err = f.WriteString("release\n")
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
		if err == nil {
			err = waitCleanupFile(ctx, finished)
		}
		controllerDone <- err
	}()

	t.Run("owned writer", func(t *testing.T) {
		e := newEnv(t, bashShell)
		rc := fmt.Sprintf(`HISTFILE="$HOME/.bash_history"
exec 9<>%s
__cleanup_history_on_exit() {
  printf ready >%s
  IFS= read -r -t 3 _ <&9
  history -w
  printf '%%s\n' "$?" >%s
}
trap __cleanup_history_on_exit EXIT
`, cleanupShellQuote(gate), cleanupShellQuote(ready), cleanupShellQuote(finished))
		if err := os.WriteFile(filepath.Join(e.home, ".bashrc"), []byte(rc), 0o600); err != nil {
			t.Fatal(err)
		}
		if env, code := pmx(t, e, "new", "--name", "history-writer"); code != 0 || !env.OK {
			t.Fatalf("new history writer: code=%d env=%+v", code, env)
		}
		if env, code := pmx(t, e, "run", "history-writer", "printf cleanup-history-marker"); code != 0 || env.Status != "done" {
			t.Fatalf("run history writer: code=%d env=%+v", code, env)
		}
		if killBeforeCleanup {
			if env, code := pmx(t, e, "kill", "history-writer"); code != 0 || env.Status != "killed" {
				t.Fatalf("kill history writer: code=%d env=%+v", code, env)
			}
			if err := waitCleanupFile(ctx, ready); err != nil {
				t.Fatal(err)
			}
			// Remove the bootstrap too: the endpoint may disappear while the
			// already-removed managed shell is still writing its exit history.
			if out, err := cleanupTmux(ctx, e, "kill-server"); err != nil {
				t.Fatalf("kill owned bootstrap server: %v: %s", err, out)
			}
		}
		// This runs immediately before newEnv's cleanup in the LIFO chain,
		// not when kill was issued or when the exit trap first became ready.
		t.Cleanup(func() { close(cleanupStarted) })
	})
	if _, err := os.Stat(finished); err != nil {
		t.Errorf("cleanup returned before owned exit/history writer finished: %v", err)
	}
	close(cleanupReturned)
	if err := <-controllerDone; err != nil {
		t.Fatal(err)
	}
	status, err := os.ReadFile(finished)
	if err != nil || strings.TrimSpace(string(status)) != "0" {
		t.Fatalf("history write after cleanup: status=%q err=%v", status, err)
	}
}

func waitCleanupFile(ctx context.Context, path string) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", path, ctx.Err())
		case <-ticker.C:
		}
	}
}

func cleanupShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
