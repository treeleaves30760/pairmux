//go:build unix

package paneexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for the pairmux executable: run with
// Command as its first argument, it is the helper.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == Command {
		os.Exit(Main(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// helperCmd runs the helper with own as its (the "tmux server's") environment.
func helperCmd(t *testing.T, own []string, o Options) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := Argv(exe, o)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = own
	return cmd
}

func TestMainExecsWithHandedOffEnvironment(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".pane-env")
	if err := WriteEnvFile(envFile, Env(
		[]string{"PATH=/usr/bin:/bin", "PMX_FRESH=2", "TERM=caller-term", "SECRET=line1\nline2"},
		map[string]string{"PAIRMUX_NAME": "t1"})); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(dir, ".start-gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	own := []string{"PATH=/stale", "PMX_STALE=1", "TMUX=/tmp/s,1,0", "TMUX_PANE=%5", "TERM=tmux-256color"}
	cmd := helperCmd(t, own, Options{EnvFile: envFile, Gate: gate, Argv: []string{"env"}})
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("helper ran before the gate was released: %v\n%s", err, out.String())
	case <-time.After(200 * time.Millisecond):
	}
	f, err := os.OpenFile(gate, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("go\n")
	_ = f.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper: %v\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("helper never finished")
	}
	var got []string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "=") {
			got = append(got, line)
		}
	}
	sort.Strings(got)
	want := []string{"PAIRMUX_NAME=t1", "PATH=/usr/bin:/bin", "PMX_FRESH=2", "SECRET=line1", "TERM=tmux-256color", "TMUX=/tmp/s,1,0", "TMUX_PANE=%5"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("program environment =\n %q\nwant\n %q", got, want)
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatalf("env file survived the hand-off: %v", err)
	}
}

func TestMainLoginShellAndExitStatus(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".pane-env")
	if err := WriteEnvFile(envFile, []string{"PATH=/usr/bin:/bin"}); err != nil {
		t.Fatal(err)
	}
	// The program's own $0 shows the login-shell argv0; exit status passes
	// straight through because the helper is replaced, not a parent.
	cmd := helperCmd(t, nil, Options{EnvFile: envFile, Login: true, Argv: []string{"/bin/sh", "-c", `echo "argv0=$0"; exit 7`}})
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
		t.Fatalf("exit = %v, want status 7\n%s", err, out)
	}
	if !strings.Contains(string(out), "argv0=-sh") {
		t.Fatalf("output %q lacks the login argv0 -sh", out)
	}
}

func TestMainRefusesWithoutEnvFile(t *testing.T) {
	cmd := helperCmd(t, nil, Options{EnvFile: filepath.Join(t.TempDir(), "missing"), Argv: []string{"/bin/echo", "ran"}})
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 126 {
		t.Fatalf("exit = %v, want 126\n%s", err, out)
	}
	if strings.Contains(string(out), "ran") {
		t.Fatalf("program ran with the server's environment: %q", out)
	}
}
