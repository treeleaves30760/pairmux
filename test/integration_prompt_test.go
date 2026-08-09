//go:build integration

// Coverage for prompt classification that does not read the prompt: the pane's
// line discipline, and the pane-startup ordering that decides whether there is
// anything in the journal to read in the first place.
package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSecretPromptWithoutRecognisableWording is the case the pattern list can
// never win: a credential prompt whose wording nobody has a pattern for. The
// terminal settles it — echo off with the kernel still assembling a line is
// what getpass does — so the refusal holds for a tool, a locale or a phrasing
// pairmux has never seen.
func TestSecretPromptWithoutRecognisableWording(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	// "Widget:" matches none of the secret patterns; only the tty says so.
	env, _ := pmx(t, e, "run", "t1", "printf 'Widget: '; stty -echo; read -r x; stty echo; echo", "--timeout", "8s")
	if env.Status != "awaiting-input" {
		t.Fatalf("run: %+v, want awaiting-input", env)
	}
	joined := strings.Join(env.Next, " | ")
	if !strings.Contains(joined, "do NOT guess") {
		t.Fatalf("next = %v, want the do-not-guess refusal for an echo-off prompt", env.Next)
	}
	if strings.Contains(joined, "--text") {
		t.Fatalf("next must not offer to type a secret: %v", env.Next)
	}
	pmx(t, e, "send", "t1", "--enter")
	waitStatus(t, e, "t1", "idle", 10*time.Second)
}

// TestOpenPromptStaysAnswerable guards the other side: turning the terminal
// into evidence must not make every prompt a secret. A plain read echoes, so it
// keeps the answer path.
func TestOpenPromptStaysAnswerable(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	env, _ := pmx(t, e, "run", "t1", "printf 'Continue? [y/N] '; read -r x; echo got-$x", "--timeout", "8s")
	if env.Status != "awaiting-input" {
		t.Fatalf("run: %+v, want awaiting-input", env)
	}
	if !nextContains(env.Next, "send t1 --text") {
		t.Fatalf("next = %v, want the answer path for a non-secret prompt", env.Next)
	}
	pmx(t, e, "send", "t1", "--text", "y", "--enter")
	waitStatus(t, e, "t1", "idle", 10*time.Second)
	if env, _ := pmx(t, e, "peek", "t1"); !strings.Contains(env.Output, "got-y") {
		t.Fatalf("peek output = %q, want the answered result", env.Output)
	}
}

// TestProgramTerminalCapturesItsFirstPrompt covers the pane-startup ordering.
// tmux runs a pane's command the moment the window exists, which is before
// pipe-pane can be attached, so a program that prompts immediately used to be
// able to print into a window nothing was recording — and then sit there
// invisible for its whole life. Linux lost that race routinely and macOS won
// it, which is the worst kind of bug to own.
func TestProgramTerminalCapturesItsFirstPrompt(t *testing.T) {
	e := newEnv(t, bashShell)
	script := filepath.Join(t.TempDir(), "prompt-now.sh")
	// A real read with echo off: the terminal then classifies it the moment it
	// goes quiet, so this test measures capture rather than the inference delay.
	body := "#!/bin/sh\nprintf 'Ready> '\nstty -echo\nread -r x\nstty echo\nsleep 300\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	// Several terminals, because losing this race is a matter of timing: one
	// pass proves little, a handful in a row is evidence.
	for i, name := range []string{"p1", "p2", "p3", "p4"} {
		pmx(t, e, "new", "--name", name, "--cmd", "sh "+script)
		env := pollPeekStatus(t, e, name, "awaiting-input", 10*time.Second)
		if !strings.Contains(env.Output, "Ready>") {
			t.Fatalf("terminal %d (%s): journal = %q, want the prompt printed at startup", i, name, env.Output)
		}
	}
}
