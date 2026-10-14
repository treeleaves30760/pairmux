package shellhooks

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

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
		unixOnly    bool // Windows routes --cmd and bash through the shell itself
	}{
		{
			name:        "cmd override",
			unixOnly:    true,
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
			name:     "bash basename",
			unixOnly: true,
			shell:    "bash",
			wantArgv: func(dir string) []string {
				return []string{"bash", "--rcfile", filepath.Join(dir, "shim-bash.rc"), "-i"}
			},
			wantMode: core.ModeHooks,
			wantEnv:  func(string) map[string]string { return nil },
		},
		{
			name:     "bash absolute path",
			unixOnly: true,
			shell:    "/bin/bash",
			wantArgv: func(dir string) []string {
				return []string{"bash", "--rcfile", filepath.Join(dir, "shim-bash.rc"), "-i"}
			},
			wantMode: core.ModeHooks,
			wantEnv:  func(string) map[string]string { return nil },
		},
		{
			name:     "sh maps to bash shim",
			unixOnly: true,
			shell:    "sh",
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
			name:  "pwsh hooks via an encoded prompt wrapper",
			shell: `C:\Program Files\PowerShell\7\pwsh.exe`,
			wantArgv: func(string) []string {
				return []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-NoLogo", "-NoExit", "-EncodedCommand", EncodeCommand(pwshShim)}
			},
			wantMode: core.ModeHooks,
			wantEnv:  func(string) map[string]string { return nil },
		},
		{
			name:  "windows powershell basename",
			shell: "powershell.exe",
			wantArgv: func(string) []string {
				return []string{"powershell.exe", "-NoLogo", "-NoExit", "-EncodedCommand", EncodeCommand(pwshShim)}
			},
			wantMode: core.ModeHooks,
			wantEnv:  func(string) map[string]string { return nil },
		},
		{
			name:     "cmd sentinel with delayed expansion",
			shell:    `C:\Windows\system32\cmd.exe`,
			wantArgv: func(string) []string { return []string{`C:\Windows\system32\cmd.exe`, "/v:on", "/k"} },
			wantMode: core.ModeSentinel,
			wantEnv:  func(string) map[string]string { return cmdEnv },
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
			if tt.unixOnly && runtime.GOOS == "windows" {
				t.Skip("unix argv shape")
			}
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
	} else if perm := fi.Mode().Perm(); perm != 0700 && runtime.GOOS != "windows" {
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
	if perm := fi.Mode().Perm(); perm != want && runtime.GOOS != "windows" {
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

func TestKind(t *testing.T) {
	for in, want := range map[string]string{
		"zsh": "zsh", "/bin/bash": "bash", `C:\Program Files\PowerShell\7\pwsh.exe`: "pwsh",
		"PowerShell.EXE": "powershell", `C:\Windows\System32\CMD.EXE`: "cmd", "/usr/bin/fish": "fish",
	} {
		if got := Kind(in); got != want {
			t.Errorf("Kind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeCommandRoundTrip(t *testing.T) {
	script := "Write-Output 'héllo 中文'; $x = 1"
	raw, err := base64.StdEncoding.DecodeString(EncodeCommand(script))
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("decode: %v (%d bytes)", err, len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if got := string(utf16.Decode(units)); got != script {
		t.Fatalf("round trip = %q", got)
	}
}

func TestWindowsShellSentinels(t *testing.T) {
	for _, shell := range []string{"pwsh.exe", "powershell", `C:\x\PowerShell.exe`} {
		if got := SentinelSuffix(shell); got != powershellSentinelSuffix {
			t.Errorf("SentinelSuffix(%q) = %q", shell, got)
		}
	}
	if got := SentinelSuffix("cmd.exe"); got != cmdSentinelSuffix {
		t.Errorf("SentinelSuffix(cmd.exe) = %q", got)
	}
	// The PowerShell hook emits the same marks the bash and zsh shims do.
	for _, mark := range []string{"]133;A", "]133;D;"} {
		if !strings.Contains(pwshShim, mark) {
			t.Errorf("pwsh shim lacks %q", mark)
		}
	}
	if cmdEnv["PAIRMUX_ESC"] != "\x1b" || cmdEnv["PAIRMUX_BEL"] != "\x07" {
		t.Errorf("cmd environment = %q", cmdEnv)
	}
}

// TestWindowsSentinelBoundary: the Windows suffixes print core.SentinelBoundary
// right after the sentinel OSC, without the typed command line carrying it (the
// echo would otherwise end the run before the output), and cmd resets
// ERRORLEVEL once the sentinel has read it.
func TestWindowsSentinelBoundary(t *testing.T) {
	if !strings.Contains(cmdSentinelSuffix, `%PAIRMUX_BEL%%PAIRMUX_EOC%"`) || !strings.HasSuffix(cmdSentinelSuffix, ` & (call )`) {
		t.Errorf("cmd suffix = %q, want the boundary after BEL and a trailing (call )", cmdSentinelSuffix)
	}
	if cmdEnv["PAIRMUX_EOC"] != core.SentinelBoundary {
		t.Errorf("PAIRMUX_EOC = %q, want %q", cmdEnv["PAIRMUX_EOC"], core.SentinelBoundary)
	}
	var ps strings.Builder
	for _, r := range core.SentinelBoundary {
		ps.WriteString("$([char]0x" + strconv.FormatInt(int64(r), 16) + ")")
	}
	if !strings.Contains(powershellSentinelSuffix, "$([char]7)"+ps.String()+`")`) {
		t.Errorf("PowerShell suffix = %q, want the boundary after BEL", powershellSentinelSuffix)
	}
	for _, s := range []string{cmdSentinelSuffix, powershellSentinelSuffix} {
		if strings.Contains(s, core.SentinelBoundary) {
			t.Errorf("suffix %q types the boundary itself", s)
		}
	}
	for shell, want := range map[string]bool{
		"cmd.exe": true, `C:\Windows\System32\CMD.EXE`: true, "pwsh.exe": true, "powershell": true,
		"bash": false, "/bin/zsh": false, "fish": false, "/bin/dash": false, "": false,
	} {
		if got := SentinelHasBoundary(shell); got != want {
			t.Errorf("SentinelHasBoundary(%q) = %v, want %v", shell, got, want)
		}
	}
	if SentinelSuffix("bash") != core.SentinelSuffix || SentinelSuffix("fish") != fishSentinelSuffix {
		t.Errorf("POSIX/fish suffixes changed")
	}
}

// TestSentinelPrefix: only cmd.exe needs ERRORLEVEL cleared before the command;
// every other shell's status variable is set by the command itself.
func TestSentinelPrefix(t *testing.T) {
	for shell, want := range map[string]string{
		"cmd.exe": "(call ) & ", `C:\Windows\System32\CMD.EXE`: "(call ) & ",
		"pwsh.exe": "", "powershell": "", "bash": "", "/bin/zsh": "", "fish": "", "/bin/dash": "", "": "",
	} {
		if got := SentinelPrefix(shell); got != want {
			t.Errorf("SentinelPrefix(%q) = %q, want %q", shell, got, want)
		}
	}
}

func TestWindowsCommandOverrideUsesTheShell(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows routing")
	}
	argv, _, mode, err := Prepare(t.TempDir(), "pwsh.exe", []string{"Get-Date"})
	if err != nil || mode != core.ModeSentinel || len(argv) != 4 || argv[2] != "-EncodedCommand" || argv[3] != EncodeCommand("Get-Date") {
		t.Fatalf("argv=%q mode=%q err=%v", argv, mode, err)
	}
	argv, _, _, _ = Prepare(t.TempDir(), "cmd.exe", []string{"dir"})
	if !reflect.DeepEqual(argv, []string{"cmd.exe", "/s", "/c", "dir"}) {
		t.Fatalf("cmd argv=%q", argv)
	}
}
