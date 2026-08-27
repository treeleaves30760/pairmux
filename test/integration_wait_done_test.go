//go:build integration

// Coverage for wait's completion condition: the --human handoff that ends when
// the human answers in the pane (leaving no note and no event behind), and
// --done, the completion subscription several agents can hold at once. Shares
// the harness (TestMain, pmx, pmxAsync, newEnv, ...) with the other files.
package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/output"
)

// humanTypes puts text into the pane the way an attached human does: straight
// through tmux, with no pairmux command involved. That is the whole point of
// these tests — the journal gains output but not a single event, so nothing but
// the completion mark can tell an agent the handoff is over.
func humanTypes(t *testing.T, e tenv, name, text string) {
	t.Helper()
	humanKeys(t, e, name, "-l", text)
	humanKeys(t, e, name, "Enter")
}

// humanKeys is one raw tmux send-keys against the terminal's pane, for tests
// that need the keystroke and the Enter to be separate events.
func humanKeys(t *testing.T, e tenv, name string, keys ...string) {
	t.Helper()
	argv := append([]string{"-L", e.socket, "send-keys", "-t", paneID(t, e, name)}, keys...)
	cmd := exec.Command("tmux", argv...)
	if e.tmuxTmp != "" {
		cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+e.tmuxTmp)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux %v: %v: %s", keys, err, out)
	}
}

func paneID(t *testing.T, e tenv, name string) string {
	t.Helper()
	cmd := exec.Command("tmux", "-L", e.socket, "list-panes", "-a", "-F", "#{pane_id} #{@pairmux_name}")
	if e.tmuxTmp != "" {
		cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+e.tmuxTmp)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			return f[0]
		}
	}
	t.Fatalf("no pane for terminal %q in:\n%s", name, out)
	return ""
}

// TestWaitHumanEndsWhenHumanAnswersInPane is the regression for the handoff
// that could only end in a timeout: the agent hits a secret prompt, hands off,
// and the human types the password into the pane without leaving a note —
// recording nothing at all. What the agent was waiting for is the human, not the
// command, so the wait must return as soon as the terminal is moving again. The
// command here runs on for another 20 seconds precisely to prove the wait does
// not sit through it.
func TestWaitHumanEndsWhenHumanAnswersInPane(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	pmx(t, e, "run", "t1",
		"printf 'Password: '; read -s pw; echo; for i in 1 2 3 4 5 6 7 8 9 10; do echo step $i; sleep 2; done",
		"--timeout", "1s")
	pollPeekStatus(t, e, "t1", "awaiting-input", 10*time.Second)

	collect := pmxAsync(t, e, "wait", "t1", "--human", "--timeout", "60s")
	time.Sleep(700 * time.Millisecond) // let wait baseline before the human acts
	answered := time.Now()
	humanTypes(t, e, "t1", "hunter2")

	env, code := collect()
	elapsed := time.Since(answered)
	if code != 0 || env.Status != "running" {
		t.Fatalf("wait --human: code=%d env=%+v, want running", code, env)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("took %s after the answer: the handoff ended when the human did, not when the command does", elapsed)
	}
	// A handoff exists because the agent must not see what the human typed. The
	// span the command occupied is exactly that span, so it is never quoted back.
	if env.Output != "" {
		t.Fatalf("wait --human returned output %q; a handoff must not quote the span the human typed into", env.Output)
	}
	if !nextContains(env.Next, "wait t1 --done") {
		t.Fatalf("next = %v, want the --done hint for following the command that is still running", env.Next)
	}
	pmx(t, e, "send", "t1", "--key", "C-c")
}

// TestWaitHumanWaitsForTheAnswerNotTheTyping: keystrokes echo into the journal
// one character at a time, and an echoed answer stops the last line looking like
// a prompt long before the human commits to it. Growth alone therefore reads a
// human mid-keystroke as a human who is finished; only the newline their Enter
// produces says otherwise.
func TestWaitHumanWaitsForTheAnswerNotTheTyping(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	pmx(t, e, "run", "t1", "printf 'Continue? [y/N] '; read -r a; echo; echo answered-$a; sleep 5", "--timeout", "1s")
	pollPeekStatus(t, e, "t1", "awaiting-input", 10*time.Second)

	// The deadline is deliberately short and expires while the human is still
	// deciding: a wait that reported "moving again" off the bare keystroke would
	// come back "running" instead, well before this timeout.
	collect := pmxAsync(t, e, "wait", "t1", "--human", "--timeout", "4s")
	time.Sleep(700 * time.Millisecond)
	humanKeys(t, e, "t1", "-l", "y") // one keystroke, no Enter

	env, code := collect()
	if code != 0 || env.Status != "timeout" {
		t.Fatalf("wait --human: code=%d env=%+v, want timeout — a keystroke is not an answer", code, env)
	}
	if !nextContains(env.Next, "wait t1 --human") {
		t.Fatalf("next = %v, want the same handoff wait offered again", env.Next)
	}
	// Committing to the answer does let the program move on. (Releasing the wait
	// on it is TestWaitHumanEndsWhenHumanAnswersInPane; here the echoed keystroke
	// has already made the line stop looking like a prompt, so this terminal is
	// past the state under test.)
	humanKeys(t, e, "t1", "Enter")
	waitStatus(t, e, "t1", "idle", 15*time.Second)
}

// TestWaitHumanTimeoutKeepsItsConditions: a handoff that times out has not
// failed — the agent is told to wait again. The hint it follows used to drop
// every condition flag, so the retry became a plain idle wait, which at the
// handoff prompt returns instantly and puts the agent back where it started.
func TestWaitHumanTimeoutKeepsItsConditions(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	pmx(t, e, "run", "t1", "printf 'Password: '; read -s pw", "--timeout", "1s")
	pollPeekStatus(t, e, "t1", "awaiting-input", 10*time.Second)

	env, code := pmx(t, e, "wait", "t1", "--human", "--notify", "--timeout", "3s")
	if code != 0 || env.Status != "timeout" {
		t.Fatalf("wait --human: code=%d env=%+v, want timeout", code, env)
	}
	if !nextContains(env.Next, "do NOT type the secret") {
		t.Fatalf("next = %v, want the do-not-type-it warning", env.Next)
	}
	// Same wait, longer deadline — and never below the 300s handoff floor, even
	// though this one asked for 3s.
	if !nextContains(env.Next, "pairmux wait t1 --human --notify --timeout 5m0s") {
		t.Fatalf("next = %v, want the same conditions retried at >= 300s", env.Next)
	}
	pmx(t, e, "send", "t1", "--enter")
}

// TestWaitHumanDoesNotReturnTheHandoffPrompt guards the other half of the same
// bug: arming --idle alongside --human used to return awaiting-input instantly,
// handing the agent back the very prompt it was handing off, which is a spin.
func TestWaitHumanDoesNotReturnTheHandoffPrompt(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	pmx(t, e, "run", "t1", "printf 'Password: '; read -s pw", "--timeout", "1s")
	pollPeekStatus(t, e, "t1", "awaiting-input", 10*time.Second)

	env, code := pmx(t, e, "wait", "t1", "--human", "--idle", "800", "--timeout", "3s")
	if code != 0 || env.Status != "timeout" {
		t.Fatalf("wait --human --idle at a prompt: code=%d env=%+v, want status timeout", code, env)
	}
	pmx(t, e, "send", "t1", "--enter") // unblock read -s
}

// TestWaitDoneFansOutToEverySubscriber is the subscription: two agents block on
// --done before any command exists, and the one completion mark wakes both.
func TestWaitDoneFansOutToEverySubscriber(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	subA := pmxAsync(t, e, "wait", "t1", "--done", "--timeout", "60s")
	subB := pmxAsync(t, e, "wait", "t1", "--done", "--timeout", "60s")
	time.Sleep(700 * time.Millisecond) // both must baseline before the command starts

	renv, _ := pmx(t, e, "run", "t1", "echo built; (exit 3)", "--timeout", "30s")
	if renv.Status != "done" || renv.ExitCode == nil || *renv.ExitCode != 3 {
		t.Fatalf("run: %+v", renv)
	}

	for _, sub := range []struct {
		name    string
		collect func() (output.Envelope, int)
	}{{"A", subA}, {"B", subB}} {
		env, code := sub.collect()
		if code != 0 || env.Status != "done" {
			t.Fatalf("subscriber %s: code=%d env=%+v", sub.name, code, env)
		}
		if env.ExitCode == nil || *env.ExitCode != 3 {
			t.Fatalf("subscriber %s exit_code = %v, want 3", sub.name, env.ExitCode)
		}
		if !strings.Contains(env.Output, "built") {
			t.Fatalf("subscriber %s output = %q, want the command's output", sub.name, env.Output)
		}
	}
}

// TestWaitDoneIgnoresAnAlreadyFinishedCommand covers lazy settlement: a command
// that finished during an earlier wait keeps its cmd_start unmatched until the
// next run records the end. A subscriber must read the terminal's real status,
// not the stale event, or it would be handed a result older than itself.
func TestWaitDoneIgnoresAnAlreadyFinishedCommand(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	renv, _ := pmx(t, e, "run", "t1", "sleep 1; echo late", "--timeout", "200ms")
	if renv.Status != "running" {
		t.Fatalf("run should have timed out into running: %+v", renv)
	}
	waitStatus(t, e, "t1", "idle", 10*time.Second) // it finished; nothing recorded it

	env, code := pmx(t, e, "wait", "t1", "--done", "--timeout", "3s")
	if code != 0 || env.Status != "timeout" {
		t.Fatalf("wait --done after an unsettled completion: code=%d env=%+v, want timeout", code, env)
	}
}

// TestWaitHumanSeesACompletionItArrivedTooLateFor is the same bug shifted in
// time: the agent hands off, goes and does other work, and only gets around to
// waiting once the human has already answered and the command has finished.
// Nothing recorded any of that, so the unseen completion must resolve the wait
// the way an unseen note does, not leave it blocking for a human who has gone.
func TestWaitHumanSeesACompletionItArrivedTooLateFor(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "t1")

	pmx(t, e, "run", "t1", "printf 'Password: '; read -s pw; echo; echo accepted", "--timeout", "1s")
	pollPeekStatus(t, e, "t1", "awaiting-input", 10*time.Second)
	humanTypes(t, e, "t1", "hunter2") // the human answers and leaves; no note
	waitStatus(t, e, "t1", "idle", 10*time.Second)

	start := time.Now()
	env, code := pmx(t, e, "wait", "t1", "--human", "--timeout", "20s")
	if code != 0 || env.Status != "done" {
		t.Fatalf("wait --human after the fact: code=%d env=%+v, want done", code, env)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %s: the completion was already in the journal", elapsed)
	}
	if env.ExitCode == nil || *env.ExitCode != 0 {
		t.Fatalf("exit_code = %v, want 0", env.ExitCode)
	}
	if env.Output != "" {
		t.Fatalf("output = %q; a handoff must not quote the span the human typed into", env.Output)
	}
}

// TestWaitHumanOnProgramTerminalEndsWhenThePromptClears covers the handoff a
// --cmd terminal can still hit (ssh, a login prompt): there is no shell and so
// no completion mark to wait for, and the resolution is the prompt itself going
// away. A wrong answer that re-prompts must keep the wait blocked.
func TestWaitHumanOnProgramTerminalEndsWhenThePromptClears(t *testing.T) {
	e := newEnv(t, bashShell)
	script := filepath.Join(t.TempDir(), "login.sh")
	// No opening delay: the pane's command is now held until the journal is
	// capturing, so a prompt printed the instant the program starts is caught.
	// This test failed on Linux without that, and passed on macOS by luck.
	body := "#!/bin/sh\n" +
		"printf 'Password: '\nread -r p\necho\n" +
		"if [ \"$p\" != right ]; then printf 'Sorry, try again.\\nPassword: '; read -r p; echo; fi\n" +
		"echo accepted\nsleep 300\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	pmx(t, e, "new", "--name", "p1", "--cmd", "sh "+script)
	pollPeekStatus(t, e, "p1", "awaiting-input", 10*time.Second)

	collect := pmxAsync(t, e, "wait", "p1", "--human", "--timeout", "60s")
	time.Sleep(700 * time.Millisecond)
	humanTypes(t, e, "p1", "wrong")

	// The program re-prompts: the handoff is not over, so the wait must hold.
	time.Sleep(3 * time.Second)
	if env, _ := pmx(t, e, "peek", "p1"); env.Status != "awaiting-input" {
		t.Fatalf("after the wrong password, peek = %q, want awaiting-input", env.Status)
	}
	humanTypes(t, e, "p1", "right")

	env, code := collect()
	if code != 0 || env.Status != "running" {
		t.Fatalf("wait --human on a program terminal: code=%d env=%+v, want running", code, env)
	}
	if env.Output != "" {
		t.Fatalf("output = %q; a handoff must not quote the span the human typed into", env.Output)
	}
}

// TestWaitHumanArmsOnAPromptHandedStraightFromPattern is issue #7. The
// documented hand-off loop is `wait --pattern "<the question>"` and then
// `wait --human`, and the pattern wait returns the instant the question is
// printed — inside the settle window the classifier used to require before it
// would call a terminal awaiting-input. The handoff therefore armed no
// answer-watch at all; on a --cmd terminal there is no completion mark to fall
// back on either, so it could only run out its whole deadline while the human
// answered in the pane. Note what this test does *not* do: poll for
// awaiting-input first. Every other handoff test here does, and that is exactly
// why they all passed — settling the terminal first is what hid the bug.
func TestWaitHumanArmsOnAPromptHandedStraightFromPattern(t *testing.T) {
	e := newEnv(t, bashShell)
	script := filepath.Join(t.TempDir(), "ask.sh")
	// The delay is what lets the pattern wait be armed before the question is
	// asked: --pattern only matches output past its own baseline.
	body := "#!/bin/sh\nsleep 2\nprintf 'Password: '\nread -r p\necho unlocked\nsleep 300\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	pmx(t, e, "new", "--name", "p1", "--cmd", "sh "+script)

	env, code := pmx(t, e, "wait", "p1", "--pattern", "Password", "--timeout", "20s")
	if code != 0 || env.Status != "pattern-found" {
		t.Fatalf("wait --pattern: code=%d env=%+v", code, env)
	}

	// Straight into the handoff, with no settling step between the two waits.
	collect := pmxAsync(t, e, "wait", "p1", "--human", "--timeout", "30s")
	time.Sleep(700 * time.Millisecond)
	answered := time.Now()
	humanTypes(t, e, "p1", "hunter2")

	env, code = collect()
	if code != 0 || env.Status != "running" {
		t.Fatalf("wait --human straight after --pattern: code=%d env=%+v, want running", code, env)
	}
	if elapsed := time.Since(answered); elapsed > 10*time.Second {
		t.Fatalf("took %s after the answer: the handoff ended when the human did", elapsed)
	}
	if env.Output != "" {
		t.Fatalf("output = %q; a handoff must not quote the span the human typed into", env.Output)
	}
	// The follow-on hint has to be runnable on this terminal: --done is rejected
	// on a --cmd terminal, and this is the flow that lands on one.
	if nextContains(env.Next, "--done") {
		t.Fatalf("next = %v, offers --done on a program terminal, where wait rejects it", env.Next)
	}
	if !nextContains(env.Next, "pairmux wait p1 --idle") {
		t.Fatalf("next = %v, want a way to keep following a program terminal", env.Next)
	}
}

// TestWaitHumanArmsOnAPromptThatArrivesLater is the same defect from the other
// side, and without the timing the test above depends on: here the handoff is
// armed while the command is still working, seconds before it asks anything.
// The arming decision was taken once, when the wait started, so a prompt that
// had not appeared yet could never be watched for — the wait sat blind through
// the question, the answer, and the rest of its deadline.
func TestWaitHumanArmsOnAPromptThatArrivesLater(t *testing.T) {
	e := newEnv(t, bashShell)
	script := filepath.Join(t.TempDir(), "late.sh")
	body := "#!/bin/sh\necho starting\nsleep 3\nprintf 'Password: '\nread -r p\necho unlocked\nsleep 300\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	pmx(t, e, "new", "--name", "p1", "--cmd", "sh "+script)

	// Armed on a terminal that is plainly running, not prompting.
	collect := pmxAsync(t, e, "wait", "p1", "--human", "--timeout", "40s")
	pollPeekStatus(t, e, "p1", "awaiting-input", 20*time.Second)
	answered := time.Now()
	humanTypes(t, e, "p1", "hunter2")

	env, code := collect()
	if code != 0 || env.Status != "running" {
		t.Fatalf("wait --human armed before the prompt: code=%d env=%+v, want running", code, env)
	}
	if elapsed := time.Since(answered); elapsed > 10*time.Second {
		t.Fatalf("took %s after the answer: the handoff ended when the human did", elapsed)
	}
}

// TestWaitDoneRejectsProgramTerminal: a --cmd terminal runs no shell, so it
// emits no completion marks and --done could never be satisfied. Say so.
func TestWaitDoneRejectsProgramTerminal(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx(t, e, "new", "--name", "p1", "--cmd", "cat")

	env, code := pmx(t, e, "wait", "p1", "--done", "--timeout", "3s")
	if code == 0 || env.Error == nil || env.Error.Code != "E_BAD_ARGS" {
		t.Fatalf("wait --done on a program terminal: code=%d env=%+v", code, env)
	}
	if !strings.Contains(env.Error.Hint, "--pattern") {
		t.Fatalf("hint = %q, want a workable alternative", env.Error.Hint)
	}
}
