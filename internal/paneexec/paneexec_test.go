package paneexec

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/muxd"
)

func TestEnvPrecedenceAndPaneProvidedDropped(t *testing.T) {
	environ := []string{
		"PATH=/caller/bin",
		"API_KEY=caller-secret",
		"PAIRMUX_NAME=outer",
		"TMUX=/tmp/tmux-1/other,1,0", // the caller's own tmux: never the pane's
		"TMUX_PANE=%7",
		"TERM=xterm-kitty",
		"COLORTERM=truecolor",
		"PWD=/caller/cwd",
		muxd.BuiltinPaneEnv + "=sock/%1",
		"MALFORMED",
		"=novalue",
	}
	extra := map[string]string{"PAIRMUX_NAME": "inner", "ZDOTDIR": "/shim"}
	got := Env(environ, extra)
	want := []string{"PATH=/caller/bin", "API_KEY=caller-secret", "PAIRMUX_NAME=inner", "ZDOTDIR=/shim"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Env =\n %q\nwant\n %q", got, want)
	}
}

func TestFinalKeepsPaneValuesDropsStale(t *testing.T) {
	fileEnv := []string{"PATH=/caller/bin", "PMX_FRESH=2", "TERM=leaked"}
	// The helper's own environment is the tmux server's global environment
	// plus what tmux set for this pane.
	own := []string{
		"PATH=/stale/bin",
		"PMX_STALE=1",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:9/proxy",
		"TMUX=/tmp/tmux-1/pairmux,42,0",
		"TMUX_PANE=%3",
		"TERM=tmux-256color",
		"PWD=/work",
	}
	got := Final(fileEnv, own)
	want := []string{
		"PATH=/caller/bin", "PMX_FRESH=2",
		"TMUX=/tmp/tmux-1/pairmux,42,0", "TMUX_PANE=%3", "TERM=tmux-256color", "PWD=/work",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Final =\n %q\nwant\n %q", got, want)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	env := []string{"A=1", "MULTI=line1\nline2", "EMPTY=", "EQ=a=b"}
	got, err := Decode(Encode(env))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, env) {
		t.Fatalf("round trip = %q, want %q", got, env)
	}
	if got, err := Decode(nil); err != nil || len(got) != 0 {
		t.Fatalf("Decode(nil) = %q, %v", got, err)
	}
	for _, bad := range []string{"A=1", "A=1\x00noequals\x00", "\x00"} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Errorf("Decode(%q) accepted malformed input", bad)
		}
	}
	// The error must not echo the entry, which may be part of a secret.
	if _, err := Decode([]byte("sk-secret-fragment\x00")); err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("Decode error leaks the entry: %v", err)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    Options
		wantErr bool
	}{
		{name: "minimal", args: []string{"--env-file", "/d/.pane-env", "--", "zsh", "-i"},
			want: Options{EnvFile: "/d/.pane-env", Argv: []string{"zsh", "-i"}}},
		{name: "gate and login", args: []string{"--env-file", "F", "--gate", "G", "--login", "--", "/bin/zsh"},
			want: Options{EnvFile: "F", Gate: "G", Login: true, Argv: []string{"/bin/zsh"}}},
		{name: "argv keeps flag-like words", args: []string{"--env-file", "F", "--", "sh", "--gate", "--", "x"},
			want: Options{EnvFile: "F", Argv: []string{"sh", "--gate", "--", "x"}}},
		{name: "missing env file", args: []string{"--", "sh"}, wantErr: true},
		{name: "env file without value", args: []string{"--env-file"}, wantErr: true},
		{name: "empty gate", args: []string{"--env-file", "F", "--gate", "", "--", "sh"}, wantErr: true},
		{name: "no command", args: []string{"--env-file", "F", "--"}, wantErr: true},
		{name: "no separator", args: []string{"--env-file", "F"}, wantErr: true},
		{name: "unknown flag", args: []string{"--env-file", "F", "--bogus", "--", "sh"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %+v, want error", tt.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.args, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Parse = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestArgvParsesBack(t *testing.T) {
	o := Options{EnvFile: "/d/.pane-env", Gate: "/d/.start-gate", Login: true, Argv: []string{"/bin/zsh", "-x"}}
	argv := Argv("/usr/bin/pairmux", o)
	if argv[0] != "/usr/bin/pairmux" || argv[1] != Command {
		t.Fatalf("argv = %q", argv)
	}
	got, err := Parse(argv[2:])
	if err != nil || !reflect.DeepEqual(got, o) {
		t.Fatalf("Parse(Argv(o)) = %+v, %v; want %+v", got, err, o)
	}
}

func TestWriteEnvFileIsPrivateAndConsumedOnRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".pane-env")
	if err := os.WriteFile(path, []byte("leftover"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"API_KEY=s3cret", "A=1"}
	if err := WriteEnvFile(path, env); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("env file mode = %o, want 600", perm)
		}
	}
	got, err := readEnvFile(path)
	if err != nil || !reflect.DeepEqual(got, env) {
		t.Fatalf("readEnvFile = %q, %v", got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("env file survived its read: %v", err)
	}
	if err := WriteEnvFile(filepath.Join(dir, "missing", "f"), env); err == nil {
		t.Fatal("WriteEnvFile into a missing directory succeeded")
	}
}

func TestLookPathUsesTheGivenPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execvp-style lookup is Unix-only")
	}
	dir := t.TempDir()
	prog := filepath.Join(dir, "pmx-prog")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := lookPath("pmx-prog", []string{"PATH=/nonexistent:" + dir})
	if err != nil || got != prog {
		t.Fatalf("lookPath = %q, %v; want %q", got, err, prog)
	}
	if _, err := lookPath("pmx-prog", []string{"PATH=/nonexistent"}); err == nil {
		t.Fatal("lookPath found a program outside the given PATH")
	}
	if got, _ := lookPath("/abs/prog", nil); got != "/abs/prog" {
		t.Fatalf("lookPath(absolute) = %q", got)
	}
}

func TestExecutableRefusesTestBinary(t *testing.T) {
	if _, ok := Executable(); ok {
		t.Fatal("Executable reported a helper before Enable")
	}
	Enable()
	defer enabled.Store(false)
	if exe, ok := Executable(); ok {
		t.Fatalf("Executable offered the test binary %q as the helper", exe)
	}
}
