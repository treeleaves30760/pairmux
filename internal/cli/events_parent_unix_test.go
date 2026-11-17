//go:build !windows

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/output"
)

// These parts of the --exit-with-parent and interrupted-wait tests are Unix
// only: they deliver SIGINT to a child, and test the Unix parent-pid rule.

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// An events --wait stopped by a signal before anything changed replies
// interrupted (exit 0), not timeout: its deadline never came, so "wait longer"
// is the wrong advice.
func TestEventsWaitInterruptedIsNotTimeout(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "wait.out")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEventsHelperProcess$")
	cmd.Env = append(withoutEnv(withoutEnv(withoutEnv(os.Environ(), ScopeEnv), NameEnv), "PAIRMUX_SOCKET"),
		eventsHelperEnv+"=events",
		"PAIRMUX_TEST_EVENTS_OUT="+out,
		"PAIRMUX_TEST_EVENTS_ARGS=--json events --wait --interval 100ms --timeout 30s",
		"PAIRMUX_BACKEND=builtin",
		"PAIRMUX_STATE_DIR="+filepath.Join(dir, "state"),
		"PAIRMUX_SOCKET=pmx-unit-evint-"+strconv.Itoa(os.Getpid()),
	)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // past the first listing and signal.Notify
	cmd.Process.Signal(os.Interrupt)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("interrupted events --wait: %v (want exit 0)\n%s", err, stdout.String())
	}
	var e output.Envelope
	if err := json.Unmarshal([]byte(stdout.String()), &e); err != nil {
		t.Fatalf("reply %q: %v", stdout.String(), err)
	}
	if !e.OK || e.Status != "interrupted" {
		t.Fatalf("status = %q, want interrupted: %+v", e.Status, e)
	}
	if n := strings.Join(e.Next, "\n"); strings.Contains(n, "--timeout") || !strings.Contains(n, "pairmux events --wait") {
		t.Fatalf("next = %q: want a plain retry, no doubled timeout", e.Next)
	}
}

// Started by pid 1 (a container's PID 1, a systemd unit, a launchd agent),
// the parent is not gone: pid 1 outlives us, so --exit-with-parent has
// nothing to observe and the command keeps running.
func TestExitWithParentUnderPID1KeepsRunning(t *testing.T) {
	oldStart, oldGet := startPPID, getppid
	t.Cleanup(func() { startPPID, getppid = oldStart, oldGet })
	startPPID, getppid = 1, func() int { return 1 }
	gone, stop, err := watchParent()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if gone() {
		t.Fatal("a parent that is pid 1 counted as gone")
	}
	// A real parent that then dies (we are reparented) still counts.
	startPPID = 4242
	gone, _, _ = watchParent()
	if !gone() {
		t.Fatal("reparenting from 4242 to 1 was not seen")
	}
}
