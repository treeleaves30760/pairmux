package shellhooks

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
)

func TestPrepareModeSelection(t *testing.T) {
	tests := []struct {
		name        string
		shell       string
		cmdOverride []string
		wantArgv    func(dir string) []string
		wantMode    core.Mode
		wantEnv     func(dir string) map[string]string
	}{
		{
			name:        "cmd override",
			shell:       "zsh", // ignored when cmdOverride present
			cmdOverride: []string{"echo hi && ls"},
			wantArgv:    func(string) []string { return []string{"/bin/sh", "-lc", "echo hi && ls"} },
			wantMode:    core.ModeSentinel,
			wantEnv:     func(string) map[string]string { return nil },
		},
		{
			name:     "zsh basename",
			shell:    "zsh",
			wantArgv: func(string) []string { return []string{"zsh", "-i"} },
			wantMode: core.ModeHooks,
			wantEnv: func(dir string) map[string]string {
				return map[string]string{"ZDOTDIR": filepath.Join(dir, "shim-zsh"), "PAIRMUX": "1"}
			},
		},
		{
			name:     "zsh absolute path",
			shell:    "/opt/homebrew/bin/zsh",
			wantArgv: func(string) []string { return []string{"zsh", "-i"} },
			wantMode: core.ModeHooks,
			wantEnv: func(dir string) map[string]string {
				return map[string]string{"ZDOTDIR": filepath.Join(dir, "shim-zsh"), "PAIRMUX": "1"}
			},
		},
		{
			name:  "bash basename",
			shell: "bash",
			wantArgv: func(dir string) []string {
				return []string{"bash", "--rcfile", filepath.Join(dir, "shim-bash.rc"), "-i"}
			},
			wantMode: core.ModeHooks,
			wantEnv:  func(string) map[string]string { return nil },
		},
		{
			name:  "bash absolute path",
			shell: "/bin/bash",
			wantArgv: func(dir string) []string {
				return []string{"bash", "--rcfile", filepath.Join(dir, "shim-bash.rc"), "-i"}
			},
			wantMode: core.ModeHooks,
			wantEnv:  func(string) map[string]string { return nil },
		},
		{
			name:  "sh maps to bash shim",
			shell: "sh",
			wantArgv: func(dir string) []string {
				return []string{"bash", "--rcfile", filepath.Join(dir, "shim-bash.rc"), "-i"}
			},
			wantMode: core.ModeHooks,
			wantEnv:  func(string) map[string]string { return nil },
		},
		{
			name:     "fish native hooks",
			shell:    "fish",
			wantArgv: func(string) []string { return []string{"fish"} },
			wantMode: core.ModeHooks,
			wantEnv:  func(string) map[string]string { return nil },
		},
		{
			name:     "fish absolute path preserved",
			shell:    "/usr/bin/fish",
			wantArgv: func(string) []string { return []string{"/usr/bin/fish"} },
			wantMode: core.ModeHooks,
			wantEnv:  func(string) map[string]string { return nil },
		},
		{
			name:     "unknown shell absolute path preserved",
			shell:    "/usr/bin/elvish",
			wantArgv: func(string) []string { return []string{"/usr/bin/elvish"} },
			wantMode: core.ModeSentinel,
			wantEnv:  func(string) map[string]string { return nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			argv, env, mode, err := Prepare(dir, tt.shell, tt.cmdOverride)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", mode, tt.wantMode)
			}
			if want := tt.wantArgv(dir); !reflect.DeepEqual(argv, want) {
				t.Errorf("argv = %v, want %v", argv, want)
			}
			if want := tt.wantEnv(dir); !reflect.DeepEqual(env, want) {
				t.Errorf("env = %v, want %v", env, want)
			}
		})
	}
}

func TestSentinelSuffix(t *testing.T) {
	tests := []struct {
		shell string
		want  string
	}{
		{"fish", `; printf '\033]7779;p;%d\007' $status`},
		{"/opt/homebrew/bin/fish", `; printf '\033]7779;p;%d\007' $status`},
		{"bash", core.SentinelSuffix},
		{"/bin/zsh", core.SentinelSuffix},
		{"/bin/dash", core.SentinelSuffix},
	}
	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			if got := SentinelSuffix(tt.shell); got != tt.want {
				t.Fatalf("SentinelSuffix(%q) = %q, want %q", tt.shell, got, tt.want)
			}
		})
	}
}

func TestPrepareZshFiles(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := Prepare(dir, "/bin/zsh", nil); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	shimDir := filepath.Join(dir, "shim-zsh")
	if fi, err := os.Stat(shimDir); err != nil {
		t.Fatalf("shim dir: %v", err)
	} else if perm := fi.Mode().Perm(); perm != 0700 {
		t.Errorf("shim dir perm = %o, want 700", perm)
	}

	zshrc := readFile(t, filepath.Join(shimDir, ".zshrc"))
	for _, sub := range []string{
		".zshrc",                  // sources user config
		"__pairmux_first",         // first-run guard
		`133;A`, `133;C`, `133;D`, // OSC 133 marks
		"precmd", "preexec",
	} {
		if !strings.Contains(zshrc, sub) {
			t.Errorf(".zshrc missing %q", sub)
		}
	}

	zshenv := readFile(t, filepath.Join(shimDir, ".zshenv"))
	if !strings.Contains(zshenv, ".zshenv") {
		t.Errorf(".zshenv must forward to user's .zshenv, got:\n%s", zshenv)
	}

	assertPerm(t, filepath.Join(shimDir, ".zshrc"), 0600)
	assertPerm(t, filepath.Join(shimDir, ".zshenv"), 0600)
}

func TestPrepareBashFiles(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := Prepare(dir, "/bin/bash", nil); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	rc := filepath.Join(dir, "shim-bash.rc")
	body := readFile(t, rc)
	for _, sub := range []string{
		".bashrc",         // sources user config
		"PROMPT_COMMAND",  // hook mechanism
		"__pairmux_first", // first-run guard
		"__pairmux_prompt",
		`133;D`, `133;A`, `133;C`,
		"BASH_VERSINFO[0]", // version gate for the PS0 branch...
		"BASH_VERSINFO[1]", // ...covering major and minor
		"PS0=",             // command-output-start hook assignment
	} {
		if !strings.Contains(body, sub) {
			t.Errorf("shim-bash.rc missing %q", sub)
		}
	}
	assertPerm(t, rc, 0600)
}

func TestPrepareIdempotent(t *testing.T) {
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			dir := t.TempDir()
			if _, _, _, err := Prepare(dir, shell, nil); err != nil {
				t.Fatalf("first Prepare: %v", err)
			}
			snap := snapshot(t, dir)
			if _, _, _, err := Prepare(dir, shell, nil); err != nil {
				t.Fatalf("second Prepare: %v", err)
			}
			if got := snapshot(t, dir); !reflect.DeepEqual(got, snap) {
				t.Errorf("shim files changed across calls:\nfirst  %v\nsecond %v", snap, got)
			}
		})
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := fi.Mode().Perm(); perm != want {
		t.Errorf("%s perm = %o, want %o", path, perm, want)
	}
}

// snapshot maps each shim file's relative path to its contents.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}
