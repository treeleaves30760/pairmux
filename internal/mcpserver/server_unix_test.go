//go:build unix

package mcpserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func detachSession() error {
	_, err := syscall.Setsid()
	return err
}

func TestSubprocessCancellationKillsProcessGroupGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	executor := SubprocessExecutor{
		Path: os.Args[0],
		Env: append(os.Environ(),
			"GO_WANT_MCP_HELPER=1",
			"GO_MCP_HELPER_TREE=1",
			"GO_MCP_HELPER_PID_FILE="+pidFile,
		),
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := executor.Execute(ctx, []string{"-test.run=^TestMCPHelperProcess$"})
		result <- err
	}()

	grandchildPID := waitForPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(grandchildPID, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subprocess executor did not return after cancellation")
	}
	waitForProcessExit(t, grandchildPID)
}

func TestSubprocessCancellationBoundsDetachedPipeHolderWait(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "detached.pid")
	executor := SubprocessExecutor{
		Path: os.Args[0],
		Env: append(os.Environ(),
			"GO_WANT_MCP_HELPER=1",
			"GO_MCP_HELPER_DETACHED_TREE=1",
			"GO_MCP_HELPER_PID_FILE="+pidFile,
		),
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := executor.Execute(ctx, []string{"-test.run=^TestMCPHelperProcess$"})
		result <- err
	}()

	detachedPID := waitForPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(detachedPID, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subprocess executor waited indefinitely on detached inherited pipes")
	}
	if err := syscall.Kill(detachedPID, 0); err != nil {
		t.Fatalf("detached pipe holder was not alive during WaitDelay test: %v", err)
	}
	if err := syscall.Kill(detachedPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill detached pipe holder: %v", err)
	}
	waitForProcessExit(t, detachedPID)
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probe grandchild %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d remained alive after process-group cancellation", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
