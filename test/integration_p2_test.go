//go:build integration

// P2 integration coverage: wait conditions, human notes, awaiting-input
// detection (incl. the secret-prompt handoff), log --grep/--range, kill --all,
// and doctor. Shares the harness (TestMain, pmx, newEnv, ...) with
// integration_test.go.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/output"
)

// pmxAsync starts the binary without waiting. The returned collect func blocks
// until the process exits and parses its envelope; call it from the test
// goroutine.
func pmxAsync(t *testing.T, e tenv, args ...string) func() (output.Envelope, int) {
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
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}
	return func() (output.Envelope, int) {
		t.Helper()
		code := 0
		if err := cmd.Wait(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("wait %v: %v", args, err)
			}
		}
		var env output.Envelope
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &env); err != nil {
			t.Fatalf("bad JSON from %v: %v (out=%q)", args, err, out.String())
		}
		return env, code
	}
}

// pollPeekStatus polls peek until the terminal reports want, returning the
// final envelope.
func pollPeekStatus(t *testing.T, e tenv, name, want string, timeout time.Duration) output.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		env, _ := pmx(t, e, "peek", name)
		if env.Status == want {
			return env
		}
		if time.Now().After(deadline) {
			t.Fatalf("peek %s: status %q, want %q within %s", name, env.Status, want, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestWaitIdle(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")
	pmx(t, e, "run", "t1", "echo", "hi")

	env, code := pmx(t, e, "wait", "t1", "--idle", "400", "--timeout", "15s")
	if code != 0 || env.Status != "idle" {
		t.Fatalf("wait --idle: code=%d env=%+v", code, env)
	}
}

func TestWaitPattern(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	// $((40+2)) keeps the marker out of the echoed command line, so only the
	// command's OUTPUT (printed after a delay) can satisfy the pattern.
	pmx(t, e, "send", "t1", "--text", "sleep 1; echo WAITMARK$((40+2))", "--enter")
	env, code := pmx(t, e, "wait", "t1", "--pattern", "WAITMARK42", "--timeout", "20s")
	if code != 0 || env.Status != "pattern-found" {
		t.Fatalf("wait --pattern: code=%d env=%+v", code, env)
	}
	if !strings.Contains(env.Output, "WAITMARK42") {
		t.Fatalf("pattern output = %q, want the matching line", env.Output)
	}
}

func TestWaitHumanNote(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	collect := pmxAsync(t, e, "wait", "t1", "--human", "--timeout", "20s")
	time.Sleep(700 * time.Millisecond) // let wait baseline its event count first

	nenv, code := pmx(t, e, "note", "t1", "deployed", "the", "fix")
	if code != 0 || nenv.Status != "noted" {
		t.Fatalf("note: code=%d env=%+v", code, nenv)
	}

	wenv, code := collect()
	if code != 0 || wenv.Status != "human-done" {
		t.Fatalf("wait --human: code=%d env=%+v", code, wenv)
	}
	if !strings.Contains(wenv.Output, "deployed the fix") {
		t.Fatalf("wait --human output = %q, want the note text", wenv.Output)
	}
}

func TestNotesInEnvelopes(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")
	pmx(t, e, "note", "t1", "use", "port", "9999")

	// The note is unseen: peek and the next run both carry it.
	penv, _ := pmx(t, e, "peek", "t1")
	if len(penv.Notes) != 1 || penv.Notes[0] != "use port 9999" {
		t.Fatalf("peek notes = %v", penv.Notes)
	}
	lenv, _ := pmx(t, e, "ls")
	if got := notesOf(lenv, "t1"); got != 1 {
		t.Fatalf("ls notes count = %d, want 1", got)
	}

	renv, _ := pmx(t, e, "run", "t1", "echo", "ok")
	if len(renv.Notes) != 1 || renv.Notes[0] != "use port 9999" {
		t.Fatalf("run notes = %v", renv.Notes)
	}

	// That run's cmd_end consumed it: the next run carries nothing.
	renv, _ = pmx(t, e, "run", "t1", "echo", "again")
	if len(renv.Notes) != 0 {
		t.Fatalf("second run notes = %v, want none", renv.Notes)
	}
}

func TestAwaitingInputPrompt(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	env, code := pmx(t, e, "run", "t1", "printf 'Continue [y/N]? '; read ans", "--timeout", "1s")
	if code != 0 {
		t.Fatalf("run: code=%d env=%+v", code, env)
	}

	env = pollPeekStatus(t, e, "t1", "awaiting-input", 10*time.Second)
	if !strings.Contains(env.Output, "Continue [y/N]?") {
		t.Fatalf("peek output should end with the prompt: %q", env.Output)
	}
	if !nextContains(env.Next, "pairmux send t1 --text") {
		t.Fatalf("peek next = %v, want a send hint", env.Next)
	}

	// ls reports the same refined status.
	lenv, _ := pmx(t, e, "ls")
	if statusOf(lenv, "t1") != "awaiting-input" {
		t.Fatalf("ls status = %q, want awaiting-input", statusOf(lenv, "t1"))
	}

	// Answer to unblock; the terminal settles back to idle.
	pmx(t, e, "send", "t1", "--text", "y", "--enter")
	waitStatus(t, e, "t1", "idle", 8*time.Second)
}

func TestSecretPromptHandoff(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	pmx(t, e, "run", "t1", "printf 'Password: '; read -s pw", "--timeout", "1s")
	env := pollPeekStatus(t, e, "t1", "awaiting-input", 10*time.Second)

	joined := strings.Join(env.Next, " | ")
	if !strings.Contains(joined, "wait t1 --human") {
		t.Fatalf("next = %v, want the human handoff hint", env.Next)
	}
	if !strings.Contains(joined, "do NOT guess") {
		t.Fatalf("next = %v, want the do-not-guess warning", env.Next)
	}
	if strings.Contains(joined, "--text") {
		t.Fatalf("next must not suggest typing a secret: %v", env.Next)
	}

	pmx(t, e, "send", "t1", "--enter") // unblock read -s
	waitStatus(t, e, "t1", "idle", 8*time.Second)
}

func TestLogGrepAndRange(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")
	pmx(t, e, "run", "t1", `printf 'aaa\nGREPMARK-one\nbbb\nccc\n'`)

	genv, code := pmx(t, e, "log", "t1", "--grep", "GREPMARK-[a-z]+")
	if code != 0 || genv.Status != "ok" {
		t.Fatalf("log --grep: code=%d env=%+v", code, genv)
	}
	// The pure output line (not the echoed command) matches anchored, prefixed
	// with its 1-based shaped-line number.
	m := regexp.MustCompile(`(?m)^(\d+):GREPMARK-one$`).FindStringSubmatch(genv.Output)
	if m == nil {
		t.Fatalf("grep output missing numbered marker line: %q", genv.Output)
	}
	n, _ := strconv.Atoi(m[1])

	renv, code := pmx(t, e, "log", "t1", "--range", fmt.Sprintf("%d:%d", n, n+1))
	if code != 0 || renv.Status != "ok" {
		t.Fatalf("log --range: code=%d env=%+v", code, renv)
	}
	if want := "GREPMARK-one\nbbb"; renv.Output != want {
		t.Fatalf("range output = %q, want %q", renv.Output, want)
	}
}

func TestKillAll(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "a")
	pmx(t, e, "new", "--name", "b")

	env, code := pmx(t, e, "kill", "--all")
	if code != 0 || env.Status != "killed" {
		t.Fatalf("kill --all: code=%d env=%+v", code, env)
	}
	killed := strings.Split(env.Output, "\n")
	if !containsLine(killed, "a") || !containsLine(killed, "b") {
		t.Fatalf("kill --all output = %q, want both names", env.Output)
	}

	lenv, _ := pmx(t, e, "ls")
	if statusOf(lenv, "a") != "dead" || statusOf(lenv, "b") != "dead" {
		t.Fatalf("after kill --all: %+v", lenv.Terminals)
	}
}

func TestDoctorZshHooks(t *testing.T) {
	if !isExec("/bin/zsh") {
		t.Skip("/bin/zsh not available")
	}
	e := newEnv(t, "/bin/zsh")

	// doctor isolates its live probe on its own throwaway socket; we just
	// invoke it and read the tier table.
	env, code := pmx(t, e, "doctor")
	if code != 0 || !env.OK {
		t.Fatalf("doctor: code=%d env=%+v", code, env)
	}
	if !strings.Contains(env.Output, "zsh: hooks") || strings.Contains(env.Output, "zsh: hooks-degraded") {
		t.Fatalf("doctor should report the zsh tier as hooks:\n%s", env.Output)
	}
}

// --- helpers ---

func notesOf(env output.Envelope, name string) int {
	for _, r := range env.Terminals {
		if r.Name == name {
			return r.Notes
		}
	}
	return -1
}

func nextContains(next []string, want string) bool {
	for _, n := range next {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}
