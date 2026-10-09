//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/tmux"
)

func cleanupEnv(t *testing.T, e tenv) {
	t.Helper()
	if err := shutdownEnv(e); err != nil {
		t.Errorf("cleanup socket %q: %v", e.socket, err)
	}
}

// tenv is passed by value; its tracker must remain shared when commands remove
// panes before cleanup (or the endpoint disappears while their exit traps run).
type cleanupTracker struct {
	mu    sync.Mutex
	owned map[int]string // PID -> process birth identity, not command (exec is safe)
}

func (tracker *cleanupTracker) remember(captured map[int]bool, processes map[int]cleanupProcess) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.owned == nil {
		tracker.owned = make(map[int]string)
	}
	for pid, born := range tracker.owned {
		if process, ok := processes[pid]; !ok || process.born != born || !cleanupProcessLiving(process) {
			delete(tracker.owned, pid)
		}
	}
	for pid := range captured {
		if process, ok := processes[pid]; ok && cleanupProcessLiving(process) {
			tracker.owned[pid] = process.born
		}
	}
}

func (tracker *cleanupTracker) snapshot() map[int]string {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	owned := make(map[int]string, len(tracker.owned))
	for pid, born := range tracker.owned {
		owned[pid] = born
	}
	return owned
}

func trackCleanupCommand(e tenv, args []string) error {
	if e.writers == nil || len(args) == 0 {
		return nil // CLI-only tests have no tmux environment
	}
	switch args[0] {
	case "new", "kill", "run", "send":
		// Capture before destructive commands, and after new/start/exec paths.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := captureCleanupWriters(ctx, e)
		return err
	default:
		return nil
	}
}

// kill-server acknowledges the request before pane shells finish their exit
// traps/history writes. Retain this endpoint's writers across pane removal,
// then wait for them before testing.TempDir removes their HOME and state.
func shutdownEnv(e tenv) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e.writers == nil {
		e.writers = &cleanupTracker{}
	}
	server, err := captureCleanupWriters(ctx, e)
	if err != nil {
		return err
	}
	owned := e.writers.snapshot()
	if server {
		out, err := cleanupTmux(ctx, e, "kill-server")
		if err != nil && !cleanupNoServer(err, out) {
			return fmt.Errorf("kill owned server: %w: %s", err, strings.TrimSpace(out))
		}
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		processes, err := cleanupProcesses(ctx)
		if err != nil {
			return err
		}
		living := livingCleanupProcesses(owned, processes)
		if len(living) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("owned writers still alive %v: %w", living, ctx.Err())
		case <-ticker.C:
		}
	}
}

func captureCleanupWriters(ctx context.Context, e tenv) (bool, error) {
	// Negative CLI tests deliberately pass malformed sockets to assert E_BAD_ARGS.
	// Never let harness instrumentation query those endpoints ahead of the CLI.
	if !tmux.ValidSocketName(e.socket) {
		return false, nil
	}
	out, err := cleanupTmux(ctx, e, "list-panes", "-a", "-F", "#{pid} #{pane_pid} #{pane_dead}")
	server := err == nil
	if err != nil && !cleanupNoServer(err, out) {
		return false, fmt.Errorf("list owned panes: %w: %s", err, strings.TrimSpace(out))
	}
	captured := make(map[int]bool)
	if server {
		for _, row := range strings.Split(strings.TrimSpace(out), "\n") {
			fields := strings.Fields(row)
			if len(fields) != 3 {
				return false, fmt.Errorf("invalid owned pane record %q", row)
			}
			for i, value := range fields[:2] {
				pid, err := strconv.Atoi(value)
				if err != nil || pid <= 1 {
					return false, fmt.Errorf("invalid owned process PID %q", value)
				}
				if i == 0 || fields[2] == "0" {
					captured[pid] = true // a dead pane PID may already have been reused
				}
			}
			if fields[2] != "0" && fields[2] != "1" {
				return false, fmt.Errorf("invalid owned pane state %q", fields[2])
			}
		}
	}
	processes, err := cleanupProcesses(ctx)
	if err != nil {
		return false, err
	}
	// Existing retained shells can have new exit-trap children after being
	// removed from tmux. Include those as well as pipe-pane's cat/jobs.
	for pid, born := range e.writers.snapshot() {
		if process, ok := processes[pid]; ok && process.born == born && cleanupProcessLiving(process) {
			captured[pid] = true
		}
	}
	addCleanupDescendants(captured, processes)
	e.writers.remember(captured, processes)
	return server, nil
}

func cleanupTmux(ctx context.Context, e tenv, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-L", e.socket}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if e.tmuxTmp != "" {
		cmd.Env = append(cmd.Env, "TMUX_TMPDIR="+e.tmuxTmp)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func cleanupNoServer(err error, out string) bool {
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		return false
	}
	message := strings.TrimSpace(out)
	return strings.HasPrefix(message, "no server running on ") ||
		(strings.HasPrefix(message, "error connecting to ") &&
			(strings.HasSuffix(message, " (No such file or directory)") ||
				strings.HasSuffix(message, " (Connection refused)")))
}

type cleanupProcess struct {
	parent int
	state  string
	born   string // ps lstart (seconds resolution) plus UID; command may exec
}

func cleanupProcesses(ctx context.Context) (map[int]cleanupProcess, error) {
	cmd := exec.CommandContext(ctx, "ps", "-ax", "-o", "pid=,ppid=,stat=,lstart=,uid=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect owned writers: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	processes := make(map[int]cleanupProcess)
	for _, row := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(row)
		if len(fields) != 9 {
			return nil, fmt.Errorf("invalid process record %q", row)
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil || pid <= 0 || parent < 0 {
			return nil, fmt.Errorf("invalid process record %q", row)
		}
		processes[pid] = cleanupProcess{parent: parent, state: fields[2], born: strings.Join(fields[3:], " ")}
	}
	return processes, nil
}

func addCleanupDescendants(owned map[int]bool, processes map[int]cleanupProcess) {
	children := make(map[int][]int)
	for pid, process := range processes {
		children[process.parent] = append(children[process.parent], pid)
	}
	var pending []int
	for pid := range owned {
		pending = append(pending, pid)
	}
	for len(pending) > 0 {
		parent := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, pid := range children[parent] {
			if !owned[pid] {
				owned[pid] = true
				pending = append(pending, pid)
			}
		}
	}
}

func cleanupProcessLiving(process cleanupProcess) bool {
	// A zombie may remain indefinitely under a container's PID 1, but its
	// descriptors are closed and it can no longer write to test directories.
	return !strings.HasPrefix(process.state, "Z") && !strings.HasPrefix(process.state, "X")
}

func livingCleanupProcesses(owned map[int]string, processes map[int]cleanupProcess) []int {
	var living []int
	for pid, born := range owned {
		process, present := processes[pid]
		// Never wait on an unrelated process that reused a captured PID.
		if present && process.born == born && cleanupProcessLiving(process) {
			living = append(living, pid)
		}
	}
	return living
}

func TestCleanupIgnoresExitedAndZombieWriters(t *testing.T) {
	owned := map[int]string{10: "original", 11: "original", 12: "original", 13: "original", 15: "original"}
	processes := map[int]cleanupProcess{
		10: {state: "Z+", born: "original"},
		11: {state: "X", born: "original"},
		12: {state: "Ss", born: "original"},
		14: {state: "R", born: "original"}, // live but not owned
		15: {state: "R", born: "reused"},   // captured PID, different birth
	}
	if living := livingCleanupProcesses(owned, processes); len(living) != 1 || living[0] != 12 {
		t.Fatalf("living owned writers = %v, want [12]", living)
	}
}

func TestCleanupCapturesOnlyOwnedDescendants(t *testing.T) {
	owned := map[int]bool{10: true, 11: true}
	processes := map[int]cleanupProcess{
		10: {parent: 1, state: "Ss"},
		11: {parent: 10, state: "Ss"},
		12: {parent: 10, state: "S"}, // pipe-pane cat
		13: {parent: 11, state: "S"}, // foreground job
		14: {parent: 13, state: "S"},
		20: {parent: 1, state: "Ss"}, // another server
		21: {parent: 20, state: "Ss"},
	}
	addCleanupDescendants(owned, processes)
	if len(owned) != 5 || !owned[12] || !owned[13] || !owned[14] || owned[20] || owned[21] {
		t.Fatalf("captured processes = %v, want only 10..14", owned)
	}
}

func TestCleanupTrackerSharesAndPrunesWriterIdentities(t *testing.T) {
	e := tenv{writers: &cleanupTracker{}}
	copy := e
	processes := map[int]cleanupProcess{
		10: {state: "Ss", born: "original"},
		11: {state: "Z", born: "original"},
	}
	copy.writers.remember(map[int]bool{10: true, 11: true}, processes)
	if owned := e.writers.snapshot(); len(owned) != 1 || owned[10] != "original" {
		t.Fatalf("shared captured writers = %v, want live original 10", owned)
	}
	processes[10] = cleanupProcess{state: "R", born: "reused"}
	copy.writers.remember(nil, processes)
	if owned := e.writers.snapshot(); len(owned) != 0 {
		t.Fatalf("retained reused process = %v, want empty", owned)
	}
}

func TestCleanupTrackingNeverExecutesMalformedEndpoints(t *testing.T) {
	bin := t.TempDir()
	called := filepath.Join(bin, "tmux-called")
	stub := fmt.Sprintf("#!/bin/sh\nprintf called >> %s\nexit 42\n", cleanupShellQuote(called))
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, socket := range []string{"", "../escape", "/absolute", "has space", strings.Repeat("a", 65)} {
		if tmux.ValidSocketName(socket) {
			t.Fatalf("negative fixture unexpectedly valid: %q", socket)
		}
		e := tenv{socket: socket, writers: &cleanupTracker{}}
		if server, err := captureCleanupWriters(ctx, e); server || err != nil {
			t.Errorf("capture malformed endpoint %q: server=%t err=%v", socket, server, err)
		}
		if err := trackCleanupCommand(e, []string{"new", "--name", "safe"}); err != nil {
			t.Errorf("track malformed endpoint %q: %v", socket, err)
		}
		if err := shutdownEnv(e); err != nil {
			t.Errorf("shutdown unstarted malformed endpoint %q: %v", socket, err)
		}
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatalf("tmux executed for malformed endpoint: %v", err)
	}

	// Only malformed endpoints are skipped: unexpected errors at a valid
	// endpoint still fail rather than being mistaken for a missing server.
	e := tenv{socket: "pmx-valid-guard", writers: &cleanupTracker{}}
	if err := trackCleanupCommand(e, []string{"new"}); err == nil {
		t.Fatal("unexpected valid-endpoint tmux error ignored")
	}
	if _, err := os.Stat(called); err != nil {
		t.Fatalf("valid endpoint did not query tmux: %v", err)
	}
}

func TestCleanupWithoutServer(t *testing.T) {
	e := newEnv(t, bashShell)
	if err := shutdownEnv(e); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupHonorsCustomTmuxRoots(t *testing.T) {
	if !haveTmux || !isExec(bashShell) {
		t.Skip("tmux and bash required")
	}
	shortRoot := func() string {
		dir, err := os.MkdirTemp("/tmp", "pmx-cleanup-root-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(dir); err != nil {
				t.Errorf("remove owned tmux root: %v", err)
			}
		})
		return dir
	}
	// Same unique name on different roots must still identify two endpoints.
	socket := fmt.Sprintf("pmx-cleanup-%d", os.Getpid())
	newEndpoint := func(root string) tenv {
		e := tenv{state: t.TempDir(), home: t.TempDir(), socket: socket, shell: bashShell, tmuxTmp: root, writers: &cleanupTracker{}}
		t.Cleanup(func() { cleanupEnv(t, e) })
		return e
	}
	eA := newEndpoint(shortRoot())
	eB := newEndpoint(shortRoot())
	for _, e := range []tenv{eA, eB} {
		if env, code := pmx(t, e, "new", "--name", "owned"); code != 0 || !env.OK {
			t.Fatalf("new owned endpoint: code=%d env=%+v", code, env)
		}
	}
	if err := shutdownEnv(eA); err != nil {
		t.Fatal(err)
	}
	if env, code := pmx(t, eB, "peek", "owned"); code != 0 || env.Status != "idle" {
		t.Fatalf("cleanup A disturbed B: code=%d env=%+v", code, env)
	}
	if err := shutdownEnv(eA); err != nil { // repeat shutdown after actual exit
		t.Fatal(err)
	}
	if err := shutdownEnv(eB); err != nil {
		t.Fatal(err)
	}
}
