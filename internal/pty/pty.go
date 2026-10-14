// Package pty starts a program on a pseudo terminal and hands back both ends of
// the conversation: what the program writes (Read), what it should receive as
// keyboard input (Write), and its lifetime (Wait/Kill). It is the piece of
// pairmux's built-in multiplexer that tmux used to provide.
//
// Unix (darwin, linux) uses the kernel's pty driver through /dev/ptmx, with the
// program as a session leader whose controlling terminal is the pty — exactly
// how tmux and every terminal emulator start a shell, so job control, SIGWINCH,
// SIGINT from ^C and getpass's echo-off all behave as they would anywhere else.
//
// Windows uses ConPTY (CreatePseudoConsole, Windows 10 1809+). ConPTY renders
// the console a program draws into as a VT byte stream and accepts VT input,
// which is what makes the rest of pairmux — journals, OSC marks, screen
// capture — work unchanged on Windows. The process tree is placed in a job
// object so killing a terminal reliably kills everything it started.
package pty

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Options describes the program to start.
type Options struct {
	Argv []string // program and arguments; Argv[0] is resolved against Env's PATH
	Dir  string   // working directory; "" inherits the caller's
	Env  []string // the complete environment, KEY=VALUE
	Cols int      // initial size; <= 0 picks 80x24
	Rows int
}

// Pty is a running program on a pseudo terminal. Read, Write and Resize may be
// called concurrently with each other and with Wait.
type Pty interface {
	// Read returns program output. It returns io.EOF once the program has
	// exited and its remaining output has been drained.
	Read(p []byte) (int, error)
	// Write delivers keyboard input to the program.
	Write(p []byte) (int, error)
	// Resize changes the terminal size the program sees.
	Resize(cols, rows int) error
	// Pid is the program's process id.
	Pid() int
	// TTY is the path of the terminal device (e.g. /dev/ttys004); "" where the
	// platform has none (Windows).
	TTY() string
	// Wait blocks until the program exits and returns its exit status; a
	// program killed by a signal reports 128+signal like a shell would.
	Wait() (int, error)
	// Exited is closed when the program has exited.
	Exited() <-chan struct{}
	// Kill terminates the program and anything else it started on this
	// terminal. It does not wait; use Wait or Exited.
	Kill() error
	// Close releases the terminal. Call it after Read has returned EOF.
	Close() error
}

// ErrNoProgram is returned by Start when Argv is empty.
var ErrNoProgram = errors.New("pty: no program to start")

// Start starts o.Argv on a new pseudo terminal.
func Start(o Options) (Pty, error) {
	if len(o.Argv) == 0 || o.Argv[0] == "" {
		return nil, ErrNoProgram
	}
	if o.Cols <= 0 || o.Rows <= 0 {
		o.Cols, o.Rows = 80, 24
	}
	return start(o)
}

// envValue returns the last value of key in env (case-insensitively on
// Windows, where environment names are), or "".
func envValue(env []string, key string) string {
	val := ""
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == key || (runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			val = v
		}
	}
	return val
}

// lookPath resolves file the way the program's own environment would: a path
// containing a separator is used as given; a bare name is searched in env's
// PATH, not the caller's, because the terminal's environment is the one the
// user asked for. On Windows each candidate is also tried with PATHEXT's
// extensions.
func lookPath(file string, env []string) (string, error) {
	if strings.ContainsAny(file, `/\`) || filepath.IsAbs(file) {
		if isExecutable(file) {
			return file, nil
		}
		if runtime.GOOS == "windows" {
			for _, ext := range pathExts(env) {
				if isExecutable(file + ext) {
					return file + ext, nil
				}
			}
		}
		return "", &os.PathError{Op: "exec", Path: file, Err: os.ErrNotExist}
	}
	path := envValue(env, "PATH")
	if path == "" {
		path = os.Getenv("PATH")
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			// An empty PATH element means the current directory; like
			// exec.LookPath, refuse to resolve a bare name there.
			continue
		}
		cand := filepath.Join(dir, file)
		if runtime.GOOS == "windows" {
			if filepath.Ext(file) != "" && isExecutable(cand) {
				return cand, nil
			}
			for _, ext := range pathExts(env) {
				if isExecutable(cand + ext) {
					return cand + ext, nil
				}
			}
			continue
		}
		if isExecutable(cand) {
			return cand, nil
		}
	}
	return "", &os.PathError{Op: "exec", Path: file, Err: errors.New("executable file not found in PATH")}
}

func pathExts(env []string) []string {
	v := envValue(env, "PATHEXT")
	if v == "" {
		v = os.Getenv("PATHEXT")
	}
	if v == "" {
		v = ".COM;.EXE;.BAT;.CMD"
	}
	var exts []string
	for _, e := range strings.Split(strings.ToLower(v), ";") {
		if e == "" {
			continue
		}
		if e[0] != '.' {
			e = "." + e
		}
		exts = append(exts, e)
	}
	return exts
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode()&0o111 != 0
}
