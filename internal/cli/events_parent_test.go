//go:build !windows

package cli

import (
	"bufio"
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

// The helper process plays two roles, chosen by eventsHelperEnv: "parent"
// starts the "events" role (this binary again, running pairmux events) with
// stdout to a file, prints its pid and exits at once — a front end that dies
// without closing anything; "events" runs pairmux and records its exit code.
const eventsHelperEnv = "PAIRMUX_TEST_EVENTS_HELPER"

func TestEventsHelperProcess(t *testing.T) {
	switch os.Getenv(eventsHelperEnv) {
	case "parent":
		cmd := exec.Command(os.Args[0], "-test.run=^TestEventsHelperProcess$")
		cmd.Env = append(os.Environ(), eventsHelperEnv+"=events")
		out, err := os.Create(os.Getenv("PAIRMUX_TEST_EVENTS_OUT"))
		if err != nil {
			os.Exit(3)
		}
		cmd.Stdout = out
		if err := cmd.Start(); err != nil {
			os.Exit(4)
		}
		// Exit only once the child has read its parent pid: a parent that
		// dies before that is, by design, not detected (see watchParent).
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(os.Getenv("PAIRMUX_TEST_EVENTS_OUT") + ".ready"); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Stdout.WriteString(strconv.Itoa(cmd.Process.Pid) + "\n")
		os.Exit(0) // without waiting: the child is orphaned
	case "events":
		// startPPID was read during package initialisation, before this.
		_ = os.WriteFile(os.Getenv("PAIRMUX_TEST_EVENTS_OUT")+".ready", nil, 0o600)
		args := strings.Fields(os.Getenv("PAIRMUX_TEST_EVENTS_ARGS"))
		rc := Main(args)
		_ = os.WriteFile(os.Getenv("PAIRMUX_TEST_EVENTS_OUT")+".rc", []byte(strconv.Itoa(rc)), 0o600)
		os.Exit(rc)
	}
}

// startOrphanedEvents runs pairmux events as the child of a process that
// exits immediately, and returns the events process's pid and output file.
// It needs no tmux: the built-in backend lists an empty fleet without
// starting a server.
func startOrphanedEvents(t *testing.T, args string) (pid int, outFile string) {
	t.Helper()
	dir := t.TempDir()
	outFile = filepath.Join(dir, "events.out")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEventsHelperProcess$")
	cmd.Env = append(withoutEnv(withoutEnv(withoutEnv(os.Environ(), ScopeEnv), NameEnv), "PAIRMUX_SOCKET"),
		eventsHelperEnv+"=parent",
		"PAIRMUX_TEST_EVENTS_OUT="+outFile,
		"PAIRMUX_TEST_EVENTS_ARGS="+args,
		"PAIRMUX_BACKEND=builtin",
		"PAIRMUX_STATE_DIR="+filepath.Join(dir, "state"),
		"PAIRMUX_SOCKET=pmx-unit-evparent-"+strconv.Itoa(os.Getpid()),
	)
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("parent helper: %v", err)
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("parent helper printed %q", b)
	}
	return pid, outFile
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readEvents(t *testing.T, path string) []output.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var evs []output.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e output.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		evs = append(evs, e)
	}
	return evs
}

// With --exit-with-parent, an events stream whose starter died ends within a
// couple of poll intervals, says why, and exits 0.
func TestEventsExitWithParentEndsWhenParentDies(t *testing.T) {
	pid, out := startOrphanedEvents(t, "events --interval 100ms --exit-with-parent")
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("events --exit-with-parent outlived its parent")
	}
	// The rc file is written just before exit; give the filesystem a moment.
	var rc []byte
	for i := 0; i < 50; i++ {
		if rc, _ = os.ReadFile(out + ".rc"); len(rc) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(rc) != "0" {
		t.Fatalf("exit code = %q, want 0", rc)
	}
	evs := readEvents(t, out)
	if len(evs) < 2 || evs[0].Type != "snapshot" {
		t.Fatalf("events = %+v", evs)
	}
	if last := evs[len(evs)-1]; last.Type != "end" || last.Reason != "parent-exited" {
		t.Fatalf("last event = %+v, want end parent-exited", last)
	}
}

// Without the flag nothing changes: an orphaned stream keeps running (the
// nohup pairmux events > file & use).
func TestEventsWithoutExitWithParentOutlivesParent(t *testing.T) {
	pid, out := startOrphanedEvents(t, "events --interval 100ms --timeout 30s")
	defer syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(1 * time.Second) // ten poll intervals
	if !alive(pid) {
		t.Fatalf("events without --exit-with-parent ended with its parent: %+v", readEvents(t, out))
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
}

// --wait honours the flag too: an orphaned wait stops early, as interrupted.
func TestEventsWaitExitWithParent(t *testing.T) {
	pid, out := startOrphanedEvents(t, "--json events --wait --interval 100ms --timeout 30s --exit-with-parent")
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("events --wait --exit-with-parent outlived its parent")
	}
	b, _ := os.ReadFile(out)
	var e output.Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("reply %q: %v", b, err)
	}
	if e.Status != "interrupted" || strings.Contains(strings.Join(e.Next, "\n"), "--timeout") {
		t.Fatalf("reply = %+v", e)
	}
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
