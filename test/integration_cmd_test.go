//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/output"
)

// exitOf renders an envelope's exit code for failure messages.
func exitOf(env output.Envelope) string {
	if env.ExitCode == nil {
		return "nil"
	}
	return strconv.Itoa(*env.ExitCode)
}

// useCmdShell makes cmd.exe the shell of the terminals the test creates.
func useCmdShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("cmd.exe is Windows-only")
	}
	t.Setenv("PAIRMUX_SHELL", filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
}

// waitPeekContains polls peek until the terminal's output contains want.
func waitPeekContains(t *testing.T, e tenv, name, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		env, _ := pmx(t, e, "peek", name)
		if strings.Contains(env.Output, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never showed %q: %+v", name, want, env)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitPromptAfter polls the screen until a cmd.exe prompt follows the line
// that echoed typed, i.e. the command typed there has finished.
func waitPromptAfter(t *testing.T, e tenv, name, typed string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		screen, _ := pmx(t, e, "peek", name, "--screen")
		lines := strings.Split(screen.Output, "\n")
		echoed := -1
		for i, l := range lines {
			if strings.Contains(l, typed) {
				echoed = i
			}
		}
		for i := echoed + 1; echoed >= 0 && i < len(lines); i++ {
			if strings.HasSuffix(strings.TrimSpace(lines[i]), ">") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no prompt after %q:\n%s", typed, screen.Output)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestCmdSentinelRunReportsOutputAndExitCodes drives cmd.exe, which has no
// hooks and completes through the sentinel. ConPTY writes the sentinel OSC out
// at the start of its render frame, ahead of the output cmd printed in that
// frame, so without the boundary the run returned no output; and `<nul set /p`
// left ERRORLEVEL at 1 for the next builtin to report.
func TestCmdSentinelRunReportsOutputAndExitCodes(t *testing.T) {
	useCmdShell(t)
	e := newEnv(t, "")
	dumpJournalOnFailure(t, e, "c1")
	if env, code := pmx(t, e, "new", "--name", "c1"); code != 0 || !env.OK || env.Mode != "sentinel" {
		t.Fatalf("new: code=%d env=%+v", code, env)
	}

	run := func(cmd string, exit int, want string) {
		t.Helper()
		env, code := pmx(t, e, "run", "c1", cmd)
		if code != 0 || env.Status != "done" || env.ExitCode == nil || *env.ExitCode != exit {
			t.Fatalf("run %q: code=%d exit=%s env=%+v, want done exit=%d", cmd, code, exitOf(env), env, exit)
		}
		if want != "" && strings.Count(env.Output, want) != 1 {
			t.Fatalf("run %q: output %q, want %q exactly once", cmd, env.Output, want)
		}
		if strings.ContainsAny(env.Output, "\u200b\x1b") || strings.Contains(env.Output, "7779") {
			t.Fatalf("run %q: output carries sentinel residue: %q", cmd, env.Output)
		}
	}
	run("echo cmd-marker", 0, "cmd-marker")
	run("echo second-marker", 0, "second-marker") // not the stale 1 set /p leaves
	run("cmd /c exit 7", 7, "")
	run("echo after", 0, "after")

	// A command typed with send (or by a human) leaves its ERRORLEVEL behind,
	// and builtins such as echo never reset it; every run must start from 0.
	if env, code := pmx(t, e, "send", "c1", "--text", "cmd /c exit 9", "--enter"); code != 0 || env.Status != "sent" {
		t.Fatalf("send: code=%d env=%+v", code, env)
	}
	waitPromptAfter(t, e, "c1", "cmd /c exit 9", 10*time.Second)
	run("echo x", 0, "x")

	log, _ := pmx(t, e, "log", "c1", "--cmd", "1")
	if !strings.Contains(log.Output, "cmd-marker") {
		t.Fatalf("log --cmd 1 lacks the output: %q", log.Output)
	}

	screen, _ := pmx(t, e, "peek", "c1", "--screen")
	lines := map[string]bool{}
	for _, l := range strings.Split(screen.Output, "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	for _, want := range []string{"cmd-marker", "second-marker", "after"} {
		if !lines[want] {
			t.Fatalf("peek --screen lacks a %q line:\n%s", want, screen.Output)
		}
	}
	if strings.ContainsAny(screen.Output, "\u200b\x1b\ufffd") {
		t.Fatalf("peek --screen shows stray characters: %q", screen.Output)
	}
}

// The server was started with CREATE_NEW_PROCESS_GROUP, which leaves a
// process ignoring Ctrl+C, and every ConPTY child inherited that: the Ctrl+C
// key event ConPTY made of `send --key C-c` never became a CTRL_C_EVENT for
// cmd.exe or the programs it ran. PowerShell re-enables Ctrl+C for itself and
// its children, so only cmd.exe terminals showed it.

func TestCmdCtrlCStopsProgramTerminal(t *testing.T) {
	useCmdShell(t)
	e := newEnv(t, "")
	dumpJournalOnFailure(t, e, "pc")
	if env, code := pmx(t, e, "new", "--name", "pc", "--cmd", "ping -n 60 127.0.0.1"); code != 0 || !env.OK {
		t.Fatalf("new --cmd: code=%d env=%+v", code, env)
	}
	waitPeekContains(t, e, "pc", "TTL=", 15*time.Second) // ping is into its replies
	if env, code := pmx(t, e, "send", "pc", "--key", "C-c"); code != 0 || env.Status != "sent" {
		t.Fatalf("send C-c: code=%d env=%+v", code, env)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		list, _ := pmx(t, e, "ls")
		if statusOf(list, "pc") == "dead" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("C-c did not stop the program: ls=%+v", list.Terminals)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestCmdCtrlCInterruptsShellCommand(t *testing.T) {
	useCmdShell(t)
	e := newEnv(t, "")
	dumpJournalOnFailure(t, e, "t")
	if env, code := pmx(t, e, "new", "--name", "t"); code != 0 || !env.OK {
		t.Fatalf("new: code=%d env=%+v", code, env)
	}
	if env, code := pmx(t, e, "send", "t", "--text", "ping -n 60 127.0.0.1", "--enter"); code != 0 || env.Status != "sent" {
		t.Fatalf("send ping: code=%d env=%+v", code, env)
	}
	waitPeekContains(t, e, "t", "TTL=", 15*time.Second)
	if env, code := pmx(t, e, "send", "t", "--key", "C-c"); code != 0 || env.Status != "sent" {
		t.Fatalf("send C-c: code=%d env=%+v", code, env)
	}
	// exit_code 0, not ping's Ctrl+C exit status (0xC000013A) that echo leaves
	// in ERRORLEVEL: the run clears it first.
	env, code := pmx(t, e, "run", "t", "echo after-ctrlc", "--timeout", "15s")
	if code != 0 || env.Status != "done" || env.ExitCode == nil || *env.ExitCode != 0 || !strings.Contains(env.Output, "after-ctrlc") {
		t.Fatalf("run after C-c: code=%d exit=%s env=%+v", code, exitOf(env), env)
	}
}
