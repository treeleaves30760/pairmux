//go:build windows

package cli

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// These tests exercise the Windows parent watch directly: a handle to the
// parent process, polled with WaitForSingleObject, guarded against pid reuse
// by comparing creation times. None of them skip: on Windows each must run.

func alive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation|processSynchronize, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	ev, err := syscall.WaitForSingleObject(h, 0)
	return err == nil && ev == syscall.WAIT_TIMEOUT
}

// startSleeper starts a child that lives until killed: this test binary,
// in its "sleep" helper role.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestEventsHelperProcess$")
	cmd.Env = append(os.Environ(), eventsHelperEnv+"=sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

// A live process is watched until it exits, and then counts as gone.
func TestWatchPidSeesAProcessExit(t *testing.T) {
	cmd := startSleeper(t)
	gone, stop, err := watchPid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("watchPid: %v", err)
	}
	defer stop()
	if gone() {
		t.Fatal("a running process counted as gone")
	}
	cmd.Process.Kill()
	cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for !gone() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !gone() {
		t.Fatal("a killed process never counted as gone")
	}
}

// A parent that has already exited when it is first inspected counts as gone.
func TestWatchPidOfAnExitedProcessIsGone(t *testing.T) {
	cmd := startSleeper(t)
	pid := cmd.Process.Pid
	cmd.Process.Kill()
	cmd.Wait() // the handle exec held is closed: the pid no longer names a process
	gone, stop, err := watchPid(pid)
	if err != nil {
		t.Fatalf("watchPid of an exited pid: %v (want: gone, no error)", err)
	}
	defer stop()
	if !gone() {
		t.Fatal("an exited parent did not count as gone")
	}
}

// The pid-reuse guard: a process created after us cannot be our parent, so a
// handle to it means our real parent died and its pid was reused. An older
// process (our own parent, which started us) passes.
func TestParentNewerGuardsAgainstPidReuse(t *testing.T) {
	cmd := startSleeper(t)
	h, err := syscall.OpenProcess(processQueryLimitedInformation|processSynchronize, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatalf("OpenProcess(child): %v", err)
	}
	defer syscall.CloseHandle(h)
	if !parentNewer(h) {
		t.Fatal("a process created after us passed as our parent")
	}
	ph, err := syscall.OpenProcess(processQueryLimitedInformation|processSynchronize, false, uint32(os.Getppid()))
	if err != nil {
		t.Fatalf("OpenProcess(parent): %v", err)
	}
	defer syscall.CloseHandle(ph)
	if parentNewer(ph) {
		t.Fatal("our real parent was taken for a reused pid")
	}
	// And watchPid applies the guard: a newer process is gone from the start.
	gone, stop, err := watchPid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !gone() {
		t.Fatal("watchPid accepted a process newer than us as the parent")
	}
}
