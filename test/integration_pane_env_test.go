//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
)

// startStaleServer starts e's tmux server from an environment the pairmux
// caller does not have: tmux snapshots it as the server's global environment,
// which is what every pane used to inherit.
func startStaleServer(t *testing.T, e tenv) {
	t.Helper()
	cmd := exec.Command("tmux", "-L", e.socket, "-f", "/dev/null",
		"new-session", "-d", "-s", core.SessionName, "-x", "220", "-y", "50")
	cmd.Env = append(os.Environ(), "PMX_STALE=1", "TMUX=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start stale server: %v: %s", err, out)
	}
}

// freshCaller makes the pairmux commands this test runs see PMX_FRESH and a
// secret, and not PMX_STALE.
func freshCaller(t *testing.T) {
	t.Helper()
	t.Setenv("PMX_STALE", "")
	os.Unsetenv("PMX_STALE") // t.Setenv restores the original afterwards
	t.Setenv("PMX_FRESH", "2")
	t.Setenv("PMX_IT_SECRET", "sk-pmx-it-secret-value")
}

func assertNoSecretOnArgv(t *testing.T, e tenv) {
	t.Helper()
	out, err := exec.Command("tmux", "-L", e.socket, "list-panes", "-a", "-F", "#{pane_start_command}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-panes: %v: %s", err, out)
	}
	if strings.Contains(string(out), "sk-pmx-it-secret-value") || strings.Contains(string(out), "PMX_FRESH=2") {
		t.Fatalf("a pane's start command carries the environment:\n%s", out)
	}
}

func assertEnvFileConsumed(t *testing.T, e tenv, name string) {
	t.Helper()
	path := filepath.Join(terminalStatePath(e, name), ".pane-env")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("environment hand-off file left behind: %v", err)
	}
}

const envProbe = `echo "fresh=${PMX_FRESH-unset} stale=${PMX_STALE-unset} secret=${PMX_IT_SECRET-unset}"`
const envWant = "fresh=2 stale=unset secret=sk-pmx-it-secret-value"

// TestShellTerminalGetsCallerEnvNotServerEnv: a shell terminal's environment
// is the creating command's, whatever the tmux server was started with.
func TestShellTerminalGetsCallerEnvNotServerEnv(t *testing.T) {
	requireTmuxBackend(t)
	e := newEnv(t, bashShell)
	startStaleServer(t, e)
	freshCaller(t)

	if env, code := pmx(t, e, "new", "--name", "t1"); code != 0 || !env.OK {
		t.Fatalf("new: code=%d env=%+v", code, env)
	}
	env, code := pmx(t, e, "run", "t1", envProbe)
	if code != 0 || env.Status != "done" {
		t.Fatalf("run: code=%d env=%+v", code, env)
	}
	if !strings.Contains(env.Output, envWant) {
		t.Fatalf("pane environment output = %q, want %q", env.Output, envWant)
	}
	// tmux's own per-pane variables are still the pane's.
	env, _ = pmx(t, e, "run", "t1", `echo "pane=${TMUX_PANE-unset} term=$TERM"`)
	if strings.Contains(env.Output, "pane=unset") || !strings.Contains(env.Output, "pane=%") {
		t.Fatalf("TMUX_PANE missing in pane: %q", env.Output)
	}
	assertNoSecretOnArgv(t, e)
	assertEnvFileConsumed(t, e, "t1")
}

// TestProgramTerminalGetsCallerEnvNotServerEnv: the same for a --cmd terminal,
// whose program is held on the start gate before it runs.
func TestProgramTerminalGetsCallerEnvNotServerEnv(t *testing.T) {
	requireTmuxBackend(t)
	e := newEnv(t, bashShell)
	startStaleServer(t, e)
	freshCaller(t)

	if env, code := pmx(t, e, "new", "--name", "p1", "--cmd", envProbe+"; sleep 30"); code != 0 || !env.OK {
		t.Fatalf("new --cmd: code=%d env=%+v", code, env)
	}
	waitPeekContains(t, e, "p1", envWant, 10*time.Second)
	assertNoSecretOnArgv(t, e)
	assertEnvFileConsumed(t, e, "p1")
}
