//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

func terminalStatePath(e tenv, name string) string {
	return filepath.Join(state.EndpointDir(e.state, e.tmuxTmp, os.Getuid(), e.socket), name)
}

func TestInvalidNamesNeverReachStateOrTmux(t *testing.T) {
	e := newEnv(t, bashShell)
	commands := [][]string{
		{"new", "--name", "../escape"},
		{"run", "/tmp/absolute", "echo", "ok"},
		{"peek", "a/b"},
		{"wait", "..", "--timeout", "1ms"},
		{"send", "../escape", "--enter"},
		{"log", "/tmp/absolute"},
		{"kill", "a/b"},
		{"attach", ".."},
		{"note", "../escape", "hello"},
	}
	for _, args := range commands {
		env, code := pmx(t, e, args...)
		if code != 1 || env.Error == nil || env.Error.Code != output.CodeBadArgs {
			t.Fatalf("%v: code=%d env=%+v, want E_BAD_ARGS", args, code, env)
		}
	}
	if err := exec.Command("tmux", "-L", e.socket, "has-session", "-t", core.SessionName).Run(); err == nil {
		t.Fatal("invalid-name commands unexpectedly created a tmux session")
	}
	entries, err := os.ReadDir(e.state)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid-name commands touched the state root: %v", entries)
	}
}

func TestInvalidSocketEnvAndGlobalFlagNeverReachStateOrTmux(t *testing.T) {
	tests := []struct {
		name   string
		socket string
		args   []string
	}{
		{name: "environment", socket: "../escape", args: []string{"new", "--name", "safe"}},
		{name: "global flag", socket: "safe-socket", args: []string{"--socket", "/absolute", "new", "--name", "safe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, bashShell)
			e.socket = tt.socket
			e.tmuxTmp = t.TempDir()
			env, code := pmx(t, e, tt.args...)
			if code != 1 || env.Error == nil || env.Error.Code != output.CodeBadArgs {
				t.Fatalf("code=%d env=%+v, want E_BAD_ARGS", code, env)
			}
			for label, dir := range map[string]string{"state": e.state, "tmux root": e.tmuxTmp} {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("invalid socket touched %s: %v", label, entries)
				}
			}
		})
	}
}

func TestSocketNamespacesShareRootWithoutSharingTerminals(t *testing.T) {
	eA := newEnv(t, bashShell)
	eB := newEnv(t, bashShell)
	eB.state = eA.state

	if env, code := pmx(t, eA, "new", "--name", "only-a"); code != 0 || !env.OK {
		t.Fatalf("new only-a: code=%d env=%+v", code, env)
	}
	if env, code := pmx(t, eB, "new", "--name", "only-b"); code != 0 || !env.OK {
		t.Fatalf("new only-b: code=%d env=%+v", code, env)
	}
	if env, code := pmx(t, eA, "new", "--name", "same"); code != 0 || !env.OK {
		t.Fatalf("new same on A: code=%d env=%+v", code, env)
	}
	if env, code := pmx(t, eB, "new", "--name", "same"); code != 0 || !env.OK {
		t.Fatalf("new same on B: code=%d env=%+v", code, env)
	}

	listA, _ := pmx(t, eA, "ls")
	listB, _ := pmx(t, eB, "ls")
	if hasTerminal(listA, "only-b") || !hasTerminal(listA, "only-a") || !hasTerminal(listA, "same") {
		t.Fatalf("socket A leaked state: %+v", listA.Terminals)
	}
	if hasTerminal(listB, "only-a") || !hasTerminal(listB, "only-b") || !hasTerminal(listB, "same") {
		t.Fatalf("socket B leaked state: %+v", listB.Terminals)
	}
	if state.SocketDir(eA.state, eA.socket) == state.SocketDir(eA.state, eB.socket) {
		t.Fatal("custom sockets resolved to the same state namespace")
	}
	for _, e := range []tenv{eA, eB} {
		if _, err := os.Stat(filepath.Join(terminalStatePath(e, "same"), "meta.json")); err != nil {
			t.Fatalf("socket %s missing namespaced meta: %v", e.socket, err)
		}
	}
	paths, code := pmx(t, eA, "run", "only-a", `printf '%s\n%s\n' "$PAIRMUX_STATE_NAMESPACE" "$PAIRMUX_TERMINAL_DIR"`)
	if code != 0 || !strings.Contains(paths.Output, state.EndpointDir(eA.state, eA.tmuxTmp, os.Getuid(), eA.socket)) ||
		!strings.Contains(paths.Output, terminalStatePath(eA, "only-a")) {
		t.Fatalf("terminal state locator env missing: code=%d output=%q", code, paths.Output)
	}
}

func TestSameSocketNameInDifferentTmuxRootsIsIsolated(t *testing.T) {
	if !haveTmux {
		t.Skip("tmux not installed; skipping integration tests")
	}
	sharedState := t.TempDir()
	socket := "pmx-same"
	shortTmp := func() string {
		dir, err := os.MkdirTemp("/tmp", "pmx-root-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	newEndpoint := func(tmp string) tenv {
		e := tenv{state: sharedState, socket: socket, home: t.TempDir(), shell: bashShell, tmuxTmp: tmp}
		t.Cleanup(func() {
			cmd := exec.Command("tmux", "-L", socket, "kill-server")
			cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+tmp)
			_ = cmd.Run()
		})
		return e
	}
	eA := newEndpoint(shortTmp())
	eB := newEndpoint(shortTmp())

	if env, code := pmx(t, eA, "new", "--name", "only-a"); code != 0 || !env.OK {
		t.Fatalf("endpoint A new: code=%d env=%+v", code, env)
	}
	if env, code := pmx(t, eB, "new", "--name", "only-b"); code != 0 || !env.OK {
		t.Fatalf("endpoint B new: code=%d env=%+v", code, env)
	}
	listA, _ := pmx(t, eA, "ls")
	listB, _ := pmx(t, eB, "ls")
	if !hasTerminal(listA, "only-a") || hasTerminal(listA, "only-b") {
		t.Fatalf("endpoint A state leaked: %+v", listA.Terminals)
	}
	if !hasTerminal(listB, "only-b") || hasTerminal(listB, "only-a") {
		t.Fatalf("endpoint B state leaked: %+v", listB.Terminals)
	}
	if state.EndpointDir(sharedState, eA.tmuxTmp, os.Getuid(), socket) ==
		state.EndpointDir(sharedState, eB.tmuxTmp, os.Getuid(), socket) {
		t.Fatal("different TMUX_TMPDIR roots resolved to one namespace")
	}
}

func TestLegacyDefaultLiveTerminalRemainsOperable(t *testing.T) {
	if !haveTmux {
		t.Skip("tmux not installed; skipping integration tests")
	}
	t.Setenv("TMUX_TMPDIR", "")
	// Never disturb a developer's real default pairmux server.
	if err := exec.Command("tmux", "-L", core.DefaultSocket, "list-sessions").Run(); err == nil {
		t.Skip("default pairmux tmux server already exists")
	}
	e := tenv{state: t.TempDir(), socket: core.DefaultSocket, home: t.TempDir(), shell: bashShell}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", core.DefaultSocket, "kill-server").Run() })

	if env, code := pmx(t, e, "new", "--name", "legacy"); code != 0 || !env.OK {
		t.Fatalf("new legacy: code=%d env=%+v", code, env)
	}
	canonical := terminalStatePath(e, "legacy")
	legacy := filepath.Join(e.state, "legacy")
	if err := os.Rename(canonical, legacy); err != nil {
		t.Fatalf("simulate pre-namespace legacy layout: %v", err)
	}

	run, code := pmx(t, e, "run", "legacy", "echo", "legacy-ok")
	if code != 0 || run.Status != "done" || !strings.Contains(run.Output, "legacy-ok") {
		t.Fatalf("run against live legacy state: code=%d env=%+v", code, run)
	}
	if noted, code := pmx(t, e, "note", "legacy", "still", "works"); code != 0 || noted.Status != "noted" {
		t.Fatalf("note against live legacy state: code=%d env=%+v", code, noted)
	}
	if killed, code := pmx(t, e, "kill", "legacy"); code != 0 || killed.Status != "killed" {
		t.Fatalf("kill against live legacy state: code=%d env=%+v", code, killed)
	}
	if _, err := os.Stat(filepath.Join(legacy, "index.jsonl")); err != nil {
		t.Fatalf("legacy journal was not retained: %v", err)
	}
}

type concurrentResult struct {
	env  output.Envelope
	code int
	err  error
	out  string
}

func invokePmx(e tenv, args ...string) concurrentResult {
	cmd := exec.Command(binPath, append([]string{"--json"}, args...)...)
	cmd.Env = append(os.Environ(),
		"PAIRMUX_STATE_DIR="+e.state,
		"PAIRMUX_SOCKET="+e.socket,
		"HOME="+e.home,
		"SHELL="+e.shell,
	)
	if e.tmuxTmp != "" {
		cmd.Env = append(cmd.Env, "TMUX_TMPDIR="+e.tmuxTmp)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			return concurrentResult{err: err, out: stdout.String() + stderr.String()}
		}
	}
	var env output.Envelope
	if decodeErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); decodeErr != nil {
		return concurrentResult{code: code, err: decodeErr, out: stdout.String() + stderr.String()}
	}
	return concurrentResult{env: env, code: code, err: nil, out: stdout.String() + stderr.String()}
}

func TestConcurrentNewSameNameCreatesExactlyOnePane(t *testing.T) {
	e := newEnv(t, bashShell)
	const contenders = 12
	start := make(chan struct{})
	results := make(chan concurrentResult, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- invokePmx(e, "new", "--name", "same")
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent new invocation failed to decode/run: %v (%s)", result.err, result.out)
		}
		if result.code == 0 && result.env.OK {
			successes++
			continue
		}
		if result.code != 1 || result.env.Error == nil || result.env.Error.Code != output.CodeExists {
			t.Fatalf("loser = code %d env %+v, want E_EXISTS", result.code, result.env)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent new calls = %d, want exactly 1", successes)
	}

	out, err := exec.Command("tmux", "-L", e.socket, "list-windows", "-t", core.SessionName, "-F", "#{window_name}").Output()
	if err != nil {
		t.Fatalf("list-windows: %v", err)
	}
	count := 0
	for _, name := range strings.Fields(string(out)) {
		if name == "same" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("tmux windows named same = %d, want 1; output=%q", count, out)
	}
	list, _ := pmx(t, e, "ls")
	if len(list.Terminals) != 1 || list.Terminals[0].Name != "same" {
		t.Fatalf("managed terminal list = %+v, want one same", list.Terminals)
	}
}

func TestWaitIdleDoesNotTreatQuietPendingCommandAsIdle(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "quiet")
	running, code := pmx(t, e, "run", "quiet", "sleep", "3", "--timeout", "200ms")
	if code != 0 || running.Status != "running" {
		t.Fatalf("run quiet sleep: code=%d env=%+v", code, running)
	}

	env, code := pmx(t, e, "wait", "quiet", "--idle", "250", "--timeout", "700ms")
	if code != 0 || env.Status != "timeout" {
		t.Fatalf("wait quiet pending: code=%d env=%+v, want timeout", code, env)
	}
	if strings.Contains(strings.Join(env.Next, " | "), "pairmux run") {
		t.Fatalf("wait suggested a new run while command was pending: %v", env.Next)
	}
}

func TestWaitIdleReturnsDelayedPromptAsAwaitingInput(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "prompt")
	running, code := pmx(t, e, "run", "prompt", "sleep 1; printf 'Continue [y/N]? '; read ans", "--timeout", "200ms")
	if code != 0 || running.Status != "running" {
		t.Fatalf("run delayed prompt: code=%d env=%+v", code, running)
	}

	env, code := pmx(t, e, "wait", "prompt", "--idle", "250", "--timeout", "4s")
	if code != 0 || env.Status != string(core.StatusAwaitingInput) {
		t.Fatalf("wait delayed prompt: code=%d env=%+v, want awaiting-input", code, env)
	}
	if !strings.Contains(env.Output, "Continue [y/N]?") {
		t.Fatalf("awaiting-input output = %q, want delayed prompt", env.Output)
	}
	joined := strings.Join(env.Next, " | ")
	if !strings.Contains(joined, "pairmux send prompt --text") || strings.Contains(joined, "pairmux run") {
		t.Fatalf("awaiting-input next = %v", env.Next)
	}
	pmx(t, e, "send", "prompt", "--text", "y", "--enter")
}

func TestPatternWaitReturnsDeadWhenPaneDies(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "doomed")
	collect := pmxAsync(t, e, "wait", "doomed", "--pattern", "never-matches", "--timeout", "10s")
	// Let the wait process finish its initial resolve before killing the pane.
	time.Sleep(300 * time.Millisecond)
	if killed, code := pmx(t, e, "kill", "doomed"); code != 0 || killed.Status != "killed" {
		t.Fatalf("kill doomed: code=%d env=%+v", code, killed)
	}
	waited, code := collect()
	if code != 0 || waited.Status != string(core.StatusDead) {
		t.Fatalf("pattern wait after pane death: code=%d env=%+v, want dead", code, waited)
	}
}

func hasTerminal(env output.Envelope, name string) bool {
	for _, term := range env.Terminals {
		if term.Name == name {
			return true
		}
	}
	return false
}
