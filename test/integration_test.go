//go:build integration

// Package integration drives the built pairmux binary against a real tmux
// server and asserts on the parsed JSON envelopes. It is gated behind the
// `integration` build tag and skips cleanly when tmux is unavailable.
package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/output"
)

var (
	binPath  string
	haveTmux bool
	sockSeq  int32
)

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("tmux"); err == nil {
		haveTmux = true
	}

	buildDir, err := os.MkdirTemp("", "pmx-it-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	binPath = filepath.Join(buildDir, "pairmux")

	wd, _ := os.Getwd()      // .../pairmux/test
	repo := filepath.Dir(wd) // .../pairmux
	build := exec.Command("go", "build", "-o", binPath, "./cmd/pairmux")
	build.Dir = repo
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build pairmux:", err)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(buildDir)
	os.Exit(code)
}

// tenv is one isolated terminal-manager environment.
type tenv struct {
	state   string
	socket  string
	home    string
	shell   string
	tmuxTmp string
}

func newEnv(t *testing.T, shell string) tenv {
	t.Helper()
	if !haveTmux {
		t.Skip("tmux not installed; skipping integration tests")
	}
	if shell != "" && !isExec(shell) {
		t.Skipf("shell %s not available", shell)
	}
	n := atomic.AddInt32(&sockSeq, 1)
	e := tenv{
		state:  t.TempDir(),
		socket: fmt.Sprintf("pmx-it-%d-%d", os.Getpid(), n),
		home:   t.TempDir(),
		shell:  shell,
	}
	t.Cleanup(func() {
		cmd := exec.Command("tmux", "-L", e.socket, "kill-server")
		if e.tmuxTmp != "" {
			cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+e.tmuxTmp)
		}
		_ = cmd.Run()
	})
	return e
}

// pmx runs the built binary (always in --json mode) and returns the parsed
// envelope, the process exit code, and raw stdout.
func pmx(t *testing.T, e tenv, args ...string) (output.Envelope, int) {
	t.Helper()
	cmd := exec.Command(binPath, append([]string{"--json"}, args...)...)
	cmd.Env = append(os.Environ(),
		"PAIRMUX_STATE_DIR="+e.state,
		"PAIRMUX_SOCKET="+e.socket,
		"HOME="+e.home,
	)
	if e.shell != "" {
		cmd.Env = append(cmd.Env, "SHELL="+e.shell)
	}
	if e.tmuxTmp != "" {
		cmd.Env = append(cmd.Env, "TMUX_TMPDIR="+e.tmuxTmp)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	code := 0
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("exec %v: %v (stderr=%q)", args, err, errb.String())
		}
	}
	var env output.Envelope
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &env); err != nil {
		t.Fatalf("bad JSON from %v: %v\nstdout=%q\nstderr=%q", args, err, out.String(), errb.String())
	}
	return env, code
}

func waitStatus(t *testing.T, e tenv, name, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		env, _ := pmx(t, e, "peek", name)
		if env.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("peek %s: status %q, want %q within %s", name, env.Status, want, timeout)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func readEvents(t *testing.T, e tenv, name string) []core.Event {
	t.Helper()
	f, err := os.Open(filepath.Join(terminalStatePath(e, name), "index.jsonl"))
	if err != nil {
		t.Fatalf("open index for %s: %v", name, err)
	}
	defer f.Close()
	var evs []core.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev core.Event
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			evs = append(evs, ev)
		}
	}
	return evs
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode()&0o111 != 0
}

const bashShell = "/bin/bash"

// --- tests ---

func TestNewReady(t *testing.T) {
	e := newEnv(t, bashShell)
	env, code := pmx(t, e, "new", "--name", "t1")
	if code != 0 || !env.OK {
		t.Fatalf("new failed: code=%d env=%+v", code, env)
	}
	if env.Status != "created" || env.Terminal != "t1" {
		t.Fatalf("unexpected: %+v", env)
	}
	if env.Mode != string(core.ModeHooks) {
		t.Fatalf("mode = %q, want hooks", env.Mode)
	}
	listed, code := pmx(t, e, "ls")
	if code != 0 || statusOf(listed, "t1") != "idle" {
		t.Fatalf("new terminal is not live in ls: code=%d env=%+v", code, listed)
	}
}

func TestRunExitCodes(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	env, code := pmx(t, e, "run", "t1", "true")
	if code != 0 || env.Status != "done" || env.ExitCode == nil || *env.ExitCode != 0 {
		t.Fatalf("run true: code=%d env=%+v", code, env)
	}

	env, code = pmx(t, e, "run", "t1", "false")
	if code != 0 { // pairmux itself succeeded even though the command failed
		t.Fatalf("run false process code = %d, want 0", code)
	}
	if env.Status != "done" || env.ExitCode == nil || *env.ExitCode != 1 {
		t.Fatalf("run false: %+v", env)
	}

	env, _ = pmx(t, e, "run", "t1", "echo", "marker42")
	if !strings.Contains(env.Output, "marker42") {
		t.Fatalf("run echo output = %q, want marker42", env.Output)
	}
}

func TestRunTruncationAndLog(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	env, _ := pmx(t, e, "run", "t1", "seq", "1", "5000")
	if env.Status != "done" {
		t.Fatalf("run seq: %+v", env)
	}
	if env.Truncated == nil || env.Truncated.OmittedLines <= 0 {
		t.Fatalf("expected truncation, got %+v", env.Truncated)
	}
	if env.Truncated.GetFull != "pairmux log t1 --cmd 1" {
		t.Fatalf("get_full = %q", env.Truncated.GetFull)
	}
	if n := len(strings.Split(env.Output, "\n")); n >= 5000 {
		t.Fatalf("run output not truncated: %d lines", n)
	}

	full, _ := pmx(t, e, "log", "t1", "--cmd", "1")
	lines := strings.Split(full.Output, "\n")
	if len(lines) < 5000 {
		t.Fatalf("log --cmd full has %d lines, want >= 5000", len(lines))
	}
	if !containsLine(lines, "1") || !containsLine(lines, "5000") {
		t.Fatalf("log --cmd missing boundary lines (has1=%v has5000=%v)",
			containsLine(lines, "1"), containsLine(lines, "5000"))
	}
	if full.Truncated != nil {
		t.Fatalf("log --cmd should not be capped for seq 5000: %+v", full.Truncated)
	}
}

func TestTimeoutAndLazySettlement(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	env, code := pmx(t, e, "run", "t1", "sleep", "3", "--timeout", "1s")
	if code != 0 || env.Status != "running" {
		t.Fatalf("run sleep timeout: code=%d env=%+v", code, env)
	}

	// Peek must report running immediately, then flip to idle once sleep ends.
	env, _ = pmx(t, e, "peek", "t1")
	if env.Status != "running" {
		t.Fatalf("peek right after timeout: %+v", env)
	}
	waitStatus(t, e, "t1", "idle", 8*time.Second)

	// The next run settles the timed-out command (writes its cmd_end) then runs.
	env, _ = pmx(t, e, "run", "t1", "echo", "settled")
	if env.Status != "done" || !strings.Contains(env.Output, "settled") {
		t.Fatalf("settling run: %+v", env)
	}
	evs := readEvents(t, e, "t1")
	if !hasEnd(evs, 1) {
		t.Fatalf("expected cmd_end for cmd 1 (sleep) after settlement; events=%+v", evs)
	}
}

func TestSendIntoCatThenCtrlC(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	env, _ := pmx(t, e, "run", "t1", "cat", "--timeout", "1s")
	if env.Status != "running" {
		t.Fatalf("run cat should be running: %+v", env)
	}

	env, _ = pmx(t, e, "send", "t1", "--text", "echohello", "--enter")
	if env.Status != "sent" {
		t.Fatalf("send: %+v", env)
	}
	time.Sleep(400 * time.Millisecond)

	env, _ = pmx(t, e, "peek", "t1")
	if !strings.Contains(env.Output, "echohello") {
		t.Fatalf("cat did not echo input; output=%q", env.Output)
	}
	if env.Status != "running" {
		t.Fatalf("cat should still be running: %+v", env)
	}

	pmx(t, e, "send", "t1", "--key", "C-c")
	waitStatus(t, e, "t1", "idle", 5*time.Second)
}

func TestKill(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	env, code := pmx(t, e, "kill", "t1")
	if code != 0 || env.Status != "killed" {
		t.Fatalf("kill: code=%d env=%+v", code, env)
	}
	// The journal is retained, so the terminal is still listed but dead.
	env, _ = pmx(t, e, "peek", "t1")
	if env.Status != string(core.StatusDead) {
		t.Fatalf("peek after kill: status %q, want dead", env.Status)
	}
}

func TestLsLifecycle(t *testing.T) {
	e := newEnv(t, bashShell)

	// Empty listing hints at `new`.
	env, _ := pmx(t, e, "ls")
	if len(env.Terminals) != 0 || len(env.Next) == 0 {
		t.Fatalf("empty ls: %+v", env)
	}

	pmx(t, e, "new", "--name", "a")
	pmx(t, e, "new", "--name", "b")
	if got := statusOf(pmx1(t, e, "ls"), "a"); got != "idle" {
		t.Fatalf("a status = %q, want idle", got)
	}

	pmx(t, e, "run", "a", "sleep", "3", "--timeout", "1s")
	env, _ = pmx(t, e, "ls")
	if statusOf(env, "a") != "running" {
		t.Fatalf("a should be running: %+v", env.Terminals)
	}
	if statusOf(env, "b") != "idle" {
		t.Fatalf("b should be idle: %+v", env.Terminals)
	}

	pmx(t, e, "kill", "b")
	env, _ = pmx(t, e, "ls")
	if statusOf(env, "b") != "dead" {
		t.Fatalf("b should be dead after kill: %+v", env.Terminals)
	}
}

func TestSentinelMode(t *testing.T) {
	e := newEnv(t, "/bin/dash")
	env, _ := pmx(t, e, "new", "--name", "d1")
	if env.Mode != string(core.ModeSentinel) {
		t.Fatalf("dash should be sentinel mode: %+v", env)
	}

	env, _ = pmx(t, e, "run", "d1", "echo", "sentinelout")
	if env.Status != "done" || env.ExitCode == nil || *env.ExitCode != 0 {
		t.Fatalf("sentinel run: %+v", env)
	}
	if !strings.Contains(env.Output, "sentinelout") {
		t.Fatalf("sentinel output = %q", env.Output)
	}

	env, _ = pmx(t, e, "run", "d1", "false")
	if env.ExitCode == nil || *env.ExitCode != 1 {
		t.Fatalf("sentinel false exit = %+v", env.ExitCode)
	}
}

func TestProgramTerminalLifecycleAndRefusesRun(t *testing.T) {
	e := newEnv(t, bashShell)
	env, code := pmx(t, e, "new", "--name", "prog", "--cmd", "cat")
	if code != 0 || !env.OK || env.Status != "running" || env.Mode != string(core.ModeSentinel) {
		t.Fatalf("new program terminal: code=%d env=%+v", code, env)
	}
	if !nextContains(env.Next, "pairmux peek prog") || !nextContains(env.Next, "pairmux send prog") || nextContains(env.Next, "pairmux run prog") {
		t.Fatalf("new program next = %v, want observation/input without run", env.Next)
	}

	env, code = pmx(t, e, "peek", "prog")
	if code != 0 || env.Status != "running" {
		t.Fatalf("peek live program: code=%d env=%+v", code, env)
	}
	if !nextContains(env.Next, "pairmux peek prog") || !nextContains(env.Next, "pairmux send prog") || nextContains(env.Next, "pairmux run prog") {
		t.Fatalf("peek live program next = %v, want observation/input without run", env.Next)
	}
	if got := statusOf(pmx1(t, e, "ls"), "prog"); got != "running" {
		t.Fatalf("ls live program status = %q, want running", got)
	}

	env, code = pmx(t, e, "run", "prog", "echo", "x")
	if code != 1 || env.OK || env.Error == nil || env.Error.Code != output.CodeBadArgs {
		t.Fatalf("run on program terminal: code=%d env=%+v", code, env)
	}

	env, code = pmx(t, e, "send", "prog", "--key", "C-d")
	if code != 0 || env.Status != "sent" {
		t.Fatalf("send EOF to program: code=%d env=%+v", code, env)
	}
	waitStatus(t, e, "prog", "dead", 5*time.Second)
	env, code = pmx(t, e, "peek", "prog")
	if code != 0 || env.Status != "dead" {
		t.Fatalf("peek exited program: code=%d env=%+v", code, env)
	}
	if !nextContains(env.Next, "pairmux log prog") || nextContains(env.Next, "pairmux run prog") {
		t.Fatalf("peek exited program next = %v, want recovery without run", env.Next)
	}
}

func TestUnknownTerminalListsNames(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "known")
	env, code := pmx(t, e, "run", "ghost", "echo", "hi")
	if code != 1 || env.Error == nil || env.Error.Code != output.CodeNoTerminal {
		t.Fatalf("run ghost: code=%d env=%+v", code, env)
	}
	if !strings.Contains(env.Error.Message, "known") {
		t.Fatalf("error should list existing names: %q", env.Error.Message)
	}
}

// --- helpers ---

func pmx1(t *testing.T, e tenv, args ...string) output.Envelope {
	env, _ := pmx(t, e, args...)
	return env
}

func statusOf(env output.Envelope, name string) string {
	for _, r := range env.Terminals {
		if r.Name == name {
			return r.Status
		}
	}
	return ""
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func hasEnd(evs []core.Event, cmdID int) bool {
	for _, ev := range evs {
		if ev.Type == core.EvCmdEnd && ev.CmdID == cmdID {
			return true
		}
	}
	return false
}
