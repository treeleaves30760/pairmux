//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These tests use the platform's default shell and commands that exist on
// every platform pairmux supports, so the same suite exercises PowerShell on
// Windows and the user's shell on macOS/Linux. They are the Windows coverage
// of the core loop: create, run with exit codes, interactive send, screen
// capture, program terminals and kill.

// dumpJournalOnFailure logs the tail of a terminal's raw byte stream when the
// test fails. On Windows the stream is ConPTY's rendering of the console, and
// whether the OSC completion marks survive it is exactly what a failure there
// needs to show.
func dumpJournalOnFailure(t *testing.T, e tenv, name string) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		raw, err := os.ReadFile(filepath.Join(endpointDir(e), name, "raw.log"))
		if err != nil {
			t.Logf("raw.log for %s: %v", name, err)
			return
		}
		if len(raw) > 4096 {
			raw = raw[len(raw)-4096:]
		}
		t.Logf("raw.log tail for %s (%d bytes shown):\n%q", name, len(raw), raw)
	})
}

func portable(unix, windows string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return unix
}

func TestPortableRunReportsOutputAndExitCodes(t *testing.T) {
	e := newEnv(t, "")
	dumpJournalOnFailure(t, e, "port")
	if env, code := pmx(t, e, "new", "--name", "port"); code != 0 || !env.OK {
		t.Fatalf("new: code=%d env=%+v", code, env)
	}
	env, code := pmx(t, e, "run", "port", "echo portable-hello")
	if code != 0 || env.Status != "done" || env.ExitCode == nil || *env.ExitCode != 0 || !strings.Contains(env.Output, "portable-hello") {
		t.Fatalf("run echo: code=%d env=%+v", code, env)
	}
	env, code = pmx(t, e, "run", "port", portable("sh -c 'exit 3'", "cmd /c exit 3"))
	if code != 0 || env.Status != "done" || env.ExitCode == nil || *env.ExitCode != 3 {
		t.Fatalf("run exit 3: code=%d env=%+v", code, env)
	}
	screen, _ := pmx(t, e, "peek", "port", "--screen")
	if !strings.Contains(screen.Output, "portable-hello") {
		t.Fatalf("peek --screen lacks earlier output: %q", screen.Output)
	}
}

func TestPortableSendAnswersAPrompt(t *testing.T) {
	e := newEnv(t, "")
	dumpJournalOnFailure(t, e, "ask")
	if env, code := pmx(t, e, "new", "--name", "ask"); code != 0 || !env.OK {
		t.Fatalf("new: code=%d env=%+v", code, env)
	}
	cmd := portable(`printf 'Name? '; read x; echo "hello-$x"`, `$x = Read-Host 'Name?'; "hello-$x"`)
	env, _ := pmx(t, e, "run", "ask", cmd, "--timeout", "3s")
	if env.Status == "done" {
		t.Fatalf("prompting command finished without input: %+v", env)
	}
	if env, code := pmx(t, e, "send", "ask", "--text", "agent", "--enter"); code != 0 || env.Status != "sent" {
		t.Fatalf("send: code=%d env=%+v", code, env)
	}
	// The answer may complete the command before any wait starts, so wait for
	// the terminal to settle rather than for a completion still to come.
	settled, _ := pmx(t, e, "wait", "ask", "--timeout", "15s")
	if settled.Status != "idle" {
		t.Fatalf("wait after answering: %+v", settled)
	}
	log, _ := pmx(t, e, "log", "ask", "--cmd", "1")
	if !strings.Contains(log.Output, "hello-agent") {
		t.Fatalf("log lacks the answered output: %q", log.Output)
	}
}

func TestPortableProgramTerminalAndKill(t *testing.T) {
	e := newEnv(t, "")
	prog := portable("echo program-ready; sleep 60", "Write-Output program-ready; Start-Sleep 60")
	if runtime.GOOS == "windows" && !strings.Contains(strings.ToLower(defaultShellForTest()), "powershell") &&
		!strings.Contains(strings.ToLower(defaultShellForTest()), "pwsh") {
		prog = "echo program-ready & ping -n 60 127.0.0.1 >NUL"
	}
	dumpJournalOnFailure(t, e, "prog")
	if env, code := pmx(t, e, "new", "--name", "prog", "--cmd", prog); code != 0 || !env.OK {
		t.Fatalf("new --cmd: code=%d env=%+v", code, env)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		env, _ := pmx(t, e, "peek", "prog")
		if strings.Contains(env.Output, "program-ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("program output never appeared: %+v", env)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if env, code := pmx(t, e, "kill", "prog"); code != 0 || env.Status != "killed" {
		t.Fatalf("kill: code=%d env=%+v", code, env)
	}
	list, _ := pmx(t, e, "ls")
	if statusOf(list, "prog") != "dead" {
		t.Fatalf("killed terminal not dead in ls: %+v", list.Terminals)
	}
}

// defaultShellForTest mirrors the CLI's Windows default closely enough to
// pick a command the program terminal's interpreter understands.
func defaultShellForTest() string {
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return "cmd.exe"
}

// TestInvalidBackendIsRejectedButHelpStillWorks covers PAIRMUX_BACKEND
// validation end to end: terminal commands fail with E_BAD_ARGS, while version
// (like help, doctor, skill and update) stays usable to diagnose it.
func TestInvalidBackendIsRejectedButHelpStillWorks(t *testing.T) {
	e := newEnv(t, "")
	run := func(args ...string) (string, int) {
		cmd := exec.Command(binPath, append([]string{"--json"}, args...)...)
		cmd.Env = append(os.Environ(),
			"PAIRMUX_STATE_DIR="+e.state, "PAIRMUX_SOCKET="+e.socket, "HOME="+e.home,
			"PAIRMUX_BACKEND=screen")
		out, err := cmd.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return string(out), code
	}
	if out, code := run("ls"); code != 1 || !strings.Contains(out, "E_BAD_ARGS") || !strings.Contains(out, "PAIRMUX_BACKEND") {
		t.Fatalf("ls with a bad backend: code=%d out=%s", code, out)
	}
	if out, code := run("version"); code != 0 || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("version with a bad backend: code=%d out=%s", code, out)
	}
}
