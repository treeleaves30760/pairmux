// Package paneexec starts the program of a tmux pane with the environment of
// the pairmux command that created it.
//
// tmux gives a new pane its server's global environment, which tmux snapshots
// from whichever process first started the server, plus any `-e K=V` the
// creating command passes. Left at that, every terminal inherits a stale world:
// a server first started inside an agent session that pointed its model API at
// a proxy hands that proxy's address to every later terminal, and the caller's
// own changes (a virtualenv, an export, a fresh API key) never reach them. The
// built-in backend already starts panes with the caller's environment
// (muxd's paneEnv); this package gives the tmux backend the same contract.
//
// The environment cannot travel on tmux's command line: `-e` would put every
// variable — API keys included — on tmux's argv, visible in ps and in the
// server's command, and tmux's -e is applied on top of, not instead of, the
// stale global environment anyway. So the creating command writes the merged
// environment to a 0600 file in the terminal's private directory and starts
// the pane as this same executable with the hidden Command:
//
//	pairmux __pane-exec --env-file F [--gate G] [--login] -- argv...
//
// The helper reads and unlinks F, waits on the start gate G when given, keeps
// the handful of variables tmux itself sets for the pane (PaneProvided), and
// replaces itself with argv via execve — so the pane's process, its
// pane_current_command, its exit status and its lifecycle are the program's
// own, exactly as when tmux ran argv directly.
package paneexec

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

// Command is the hidden subcommand that runs the helper. It is dispatched
// before backend selection and never appears in help output.
const Command = "__pane-exec"

// PaneProvided are the variables tmux sets for each pane itself. They describe
// the pane — its server, its id, the terminal tmux emulates (default-terminal),
// its working directory — so the pane's own values win over the caller's, and
// a caller's value is dropped even when tmux did not set one: the caller's TMUX
// would name another server, its TERM another terminal, its PWD another
// directory. SHELL is deliberately not here: tmux sets it to default-shell, but
// the caller's SHELL is what a nested pairmux picks its shell from, matching
// the built-in backend.
var PaneProvided = []string{"TMUX", "TMUX_PANE", "TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "PWD"}

// builtinPaneEnv is muxd.BuiltinPaneEnv, which marks a process as running in a
// built-in pane. A caller in one that creates a tmux terminal must not pass it
// on (the built-in backend's paneEnv drops it likewise): attach would take the
// tmux pane for the built-in one. Spelled out so the helper stays free of the
// built-in server's dependencies; a test pins it to the original.
const builtinPaneEnv = "PAIRMUX_BUILTIN_PANE"

func paneProvided(k string) bool {
	for _, p := range PaneProvided {
		if k == p {
			return true
		}
	}
	return false
}

// enabled is set by the CLI entry point. Only a process that is the pairmux
// executable may name its own path as the pane helper: a library user, and in
// particular a `go test` binary (which would run its whole suite when
// re-executed), must not.
var enabled atomic.Bool

// Enable declares that this process's executable dispatches Command.
func Enable() { enabled.Store(true) }

// Executable returns the program to start panes through, or false when this
// process cannot serve as the helper (see Enable).
func Executable() (string, bool) {
	if !enabled.Load() {
		return "", false
	}
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	b := strings.ToLower(filepath.Base(exe))
	if strings.HasSuffix(b, ".test") || strings.HasSuffix(b, ".test.exe") {
		return "", false
	}
	return exe, true
}

// Env is the environment a new pane starts with, before the pane-provided
// variables are added: environ (the creating command's own) without
// PaneProvided and the built-in pane marker, overlaid with extra — the same precedence as the built-in
// backend's paneEnv. Order is environ's, then extra's keys sorted.
func Env(environ []string, extra map[string]string) []string {
	merged := map[string]string{}
	var order []string
	add := func(k, v string) {
		if _, seen := merged[k]; !seen {
			order = append(order, k)
		}
		merged[k] = v
	}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || paneProvided(k) || k == builtinPaneEnv {
			continue
		}
		add(k, v)
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(k, extra[k])
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+merged[k])
	}
	return out
}

// Final is the environment the program is executed with: fileEnv (Env's
// result, carried through the env file) plus each PaneProvided variable the
// helper's own environment holds — the values tmux set for this pane.
// Everything else in own is the stale server environment and is discarded.
func Final(fileEnv, own []string) []string {
	out := make([]string, 0, len(fileEnv)+len(PaneProvided))
	for _, kv := range fileEnv {
		if k, _, _ := strings.Cut(kv, "="); !paneProvided(k) {
			out = append(out, kv)
		}
	}
	for _, kv := range own {
		if k, _, ok := strings.Cut(kv, "="); ok && paneProvided(k) {
			out = append(out, kv)
		}
	}
	return out
}

// Encode renders env as NUL-terminated K=V entries. NUL is the one byte no
// environment entry can contain, so values with newlines survive intact.
func Encode(env []string) []byte {
	var b bytes.Buffer
	for _, kv := range env {
		b.WriteString(kv)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// Decode parses Encode's output.
func Decode(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != 0 {
		return nil, errors.New("environment file is truncated")
	}
	parts := strings.Split(string(data[:len(data)-1]), "\x00")
	for _, kv := range parts {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			return nil, fmt.Errorf("malformed environment entry %q", kv)
		}
	}
	return parts, nil
}

// WriteEnvFile writes env to path, readable by the owner only. A leftover from
// a crashed create is replaced; the file itself is created exclusively so a
// link planted in its place is never followed. A partial file is removed.
func WriteEnvFile(path string, env []string) error {
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(Encode(env))
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return werr
	}
	return nil
}

// WriteTempEnvFile writes env to a fresh 0600 file in the system temp
// directory, for a create that names no file of its own.
func WriteTempEnvFile(env []string) (string, error) {
	f, err := os.CreateTemp("", "pairmux-pane-env-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_, werr := f.Write(Encode(env))
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return "", werr
	}
	return path, nil
}

// readEnvFile reads the env file and unlinks it at once: the secrets it holds
// should outlive the hand-off by as little as possible.
func readEnvFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	_ = os.Remove(path)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// Options are the helper's arguments.
type Options struct {
	EnvFile string   // required: the environment hand-off
	Gate    string   // optional start-gate FIFO to wait on before exec
	Login   bool     // run Argv[0] as a login shell (argv0 "-name"), as tmux does for its default shell
	Argv    []string // the program; required
}

// Argv is the command line that runs the helper from exe with o.
func Argv(exe string, o Options) []string {
	args := []string{exe, Command, "--env-file", o.EnvFile}
	if o.Gate != "" {
		args = append(args, "--gate", o.Gate)
	}
	if o.Login {
		args = append(args, "--login")
	}
	args = append(args, "--")
	return append(args, o.Argv...)
}

// Parse reads the helper's arguments (those after Command).
func Parse(args []string) (Options, error) {
	var o Options
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--env-file", "--gate":
			if i+1 >= len(args) || args[i+1] == "" {
				return Options{}, fmt.Errorf("%s needs a value", a)
			}
			i++
			if a == "--env-file" {
				o.EnvFile = args[i]
			} else {
				o.Gate = args[i]
			}
		case "--login":
			o.Login = true
		case "--":
			o.Argv = append([]string(nil), args[i+1:]...)
			i = len(args)
		default:
			return Options{}, fmt.Errorf("unexpected argument %q", a)
		}
	}
	if o.EnvFile == "" {
		return Options{}, errors.New("--env-file is required")
	}
	if len(o.Argv) == 0 || o.Argv[0] == "" {
		return Options{}, errors.New("no command after --")
	}
	return o, nil
}

// waitGate blocks until the creating command releases the start gate (see the
// CLI's startGate). Like the shell holder it replaces, a gate that cannot be
// read does not stop the program: the worst case is the old race, not a pane
// that never starts.
func waitGate(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = bufio.NewReader(f).ReadString('\n')
}

// lookPath resolves name against the PATH of env (not this process's, which is
// the stale server's), the way execvp would.
func lookPath(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: command not found", name)
}

// Main runs the helper; it returns only when the program could not be started.
func Main(args []string) int {
	o, err := Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pairmux %s: %v\n", Command, err)
		return 2
	}
	fileEnv, err := readEnvFile(o.EnvFile)
	if err != nil {
		// Running with the server's environment instead is exactly the failure
		// this helper exists to prevent, so refuse.
		fmt.Fprintf(os.Stderr, "pairmux %s: read environment: %v\n", Command, err)
		return 126
	}
	if o.Gate != "" {
		waitGate(o.Gate)
	}
	env := Final(fileEnv, os.Environ())
	path, err := lookPath(o.Argv[0], env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pairmux %s: %v\n", Command, err)
		return 127
	}
	argv := append([]string(nil), o.Argv...)
	if o.Login {
		argv[0] = "-" + filepath.Base(argv[0])
	}
	err = execve(path, argv, env)
	fmt.Fprintf(os.Stderr, "pairmux %s: exec %s: %v\n", Command, path, err)
	return 126
}
