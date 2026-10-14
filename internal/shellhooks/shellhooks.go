// Package shellhooks prepares the shell invocation for a new terminal. For zsh
// and bash it writes shim files (embedded below) that emit OSC 133 marks. Fish
// 4+ emits those marks natively. PowerShell (pwsh and Windows PowerShell) gets
// an inline prompt wrapper that emits the same marks. cmd.exe, unknown shells
// and explicit --cmd overrides use sentinel mode. Shim bodies expand $HOME at
// runtime, so nothing host-specific is baked into them.
package shellhooks

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/treeleaves30760/pairmux/internal/core"
)

//go:embed embed/zshrc.sh
var zshrcShim string

//go:embed embed/zshenv.sh
var zshenvShim string

//go:embed embed/bashrc.sh
var bashrcShim string

//go:embed embed/pwsh.ps1
var pwshShim string

// Kind reduces a shell path to the name the integration is chosen by:
// lowercase, without directory or a Windows .exe suffix ("C:\...\pwsh.exe" ->
// "pwsh").
func Kind(shell string) string {
	base := filepath.Base(strings.ReplaceAll(shell, `\`, "/"))
	return strings.TrimSuffix(strings.ToLower(base), ".exe")
}

func isPowerShell(kind string) bool { return kind == "pwsh" || kind == "powershell" }

// EncodeCommand renders a script for PowerShell's -EncodedCommand: base64 of
// its UTF-16LE text. Unlike a .ps1 file, an encoded command is not subject to
// the execution policy, and unlike -Command it needs no command-line quoting.
func EncodeCommand(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		b[2*i] = byte(u)
		b[2*i+1] = byte(u >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// cmdEnv carries ESC and BEL into cmd.exe's environment: the sentinel needs
// them on screen, and neither can be typed into cmd's line editor, which
// treats ESC as "clear the line". PAIRMUX_EOC carries core.SentinelBoundary
// the same way, so the echoed command line never contains the boundary itself.
var cmdEnv = map[string]string{"PAIRMUX_ESC": "\x1b", "PAIRMUX_BEL": "\x07", "PAIRMUX_EOC": core.SentinelBoundary}

// Prepare returns the argv, extra environment, and mode for launching a terminal.
//
//   - cmdOverride non-empty: run it through the platform's command runner,
//     ModeSentinel: ["/bin/sh","-lc",<cmd>] on Unix; on Windows the shell
//     itself — PowerShell with -EncodedCommand, cmd.exe with /c, a POSIX shell
//     with -lc.
//   - zsh: write the zsh shim dir and return ["zsh","-i"] with ZDOTDIR/PAIRMUX
//     env, ModeHooks.
//   - bash/sh: write the bash shim and return ["bash","--rcfile",<path>,"-i"],
//     ModeHooks.
//   - fish: run [shell], preserving an absolute/custom path, ModeHooks. Fish 4+
//     emits OSC 133 natively; callers degrade to its shell-specific sentinel
//     when the ready probe sees no marks.
//   - pwsh/powershell: [shell,"-NoLogo","-NoExit","-EncodedCommand",<shim>],
//     ModeHooks; the user's profile still loads first.
//   - cmd: [shell,"/v:on","/k"], ModeSentinel (delayed expansion lets the
//     sentinel read the command's own ERRORLEVEL).
//   - anything else: run [shell], ModeSentinel.
func Prepare(stateDir, shell string, cmdOverride []string) (argv []string, env map[string]string, mode core.Mode, err error) {
	kind := Kind(shell)
	if len(cmdOverride) > 0 {
		if runtime.GOOS != "windows" {
			return append([]string{"/bin/sh", "-lc"}, cmdOverride...), nil, core.ModeSentinel, nil
		}
		script := strings.Join(cmdOverride, " ")
		switch {
		case isPowerShell(kind):
			return []string{shell, "-NoLogo", "-EncodedCommand", EncodeCommand(script)}, nil, core.ModeSentinel, nil
		case kind == "cmd":
			return []string{shell, "/s", "/c", script}, nil, core.ModeSentinel, nil
		default:
			return []string{shell, "-lc", script}, nil, core.ModeSentinel, nil
		}
	}

	switch kind {
	case "zsh":
		shimDir := filepath.Join(stateDir, "shim-zsh")
		if err := ensureDir(shimDir); err != nil {
			return nil, nil, "", err
		}
		if err := writeShim(filepath.Join(shimDir, ".zshenv"), zshenvShim); err != nil {
			return nil, nil, "", err
		}
		if err := writeShim(filepath.Join(shimDir, ".zshrc"), zshrcShim); err != nil {
			return nil, nil, "", err
		}
		return []string{"zsh", "-i"}, map[string]string{"ZDOTDIR": shimDir, "PAIRMUX": "1"}, core.ModeHooks, nil
	case "bash", "sh":
		if err := ensureDir(stateDir); err != nil {
			return nil, nil, "", err
		}
		rc := filepath.Join(stateDir, "shim-bash.rc")
		if err := writeShim(rc, bashrcShim); err != nil {
			return nil, nil, "", err
		}
		bash := "bash"
		if runtime.GOOS == "windows" {
			// Git for Windows' bash is found by its configured path, and its
			// MSYS runtime reads forward-slash drive paths reliably.
			bash = shell
			rc = filepath.ToSlash(rc)
		}
		return []string{bash, "--rcfile", rc, "-i"}, nil, core.ModeHooks, nil
	case "fish":
		return []string{shell}, nil, core.ModeHooks, nil
	case "pwsh", "powershell":
		return []string{shell, "-NoLogo", "-NoExit", "-EncodedCommand", EncodeCommand(pwshShim)}, nil, core.ModeHooks, nil
	case "cmd":
		return []string{shell, "/v:on", "/k"}, cmdEnv, core.ModeSentinel, nil
	default:
		return []string{shell}, nil, core.ModeSentinel, nil
	}
}

const fishSentinelSuffix = `; printf '\033]7779;p;%d\007' $status`

// powershellSentinelSuffix reports the previous statement's outcome the way
// the prompt hook does: 0 on success, else the native exit code, else 1. Under
// ConPTY the OSC can be hoisted ahead of the output, so core.SentinelBoundary
// follows it as text; it is spelled as [char] subexpressions so the typed (and
// echoed) line holds no boundary of its own.
//
// It is written with $Host.UI.Write, the console host's Unicode output, not
// [Console]::Write: Windows PowerShell encodes the latter with
// [Console]::OutputEncoding, an OEM code page by default, which turns the
// zero-width boundary into "???" and so hides it.
var powershellSentinelSuffix = `; $__pmx_ok = $?; $Host.UI.Write("$([char]27)]7779;p;$(if ($__pmx_ok) {0} elseif ($LASTEXITCODE) {$LASTEXITCODE} else {1})$([char]7)` +
	powershellChars(core.SentinelBoundary) + `")`

// ReadyTimeout is how long `new` waits for a hooks shell's first prompt mark
// before degrading the terminal to sentinel mode. PowerShell gets longer: its
// marks come from pairmux's own prompt wrapper, so they are reliable, but a
// cold Windows PowerShell start (profile, PSReadLine, module analysis) can
// exceed five seconds — and degrading it costs the in-order OSC 133 marks for
// the sentinel, which ConPTY may hoist. A fast shell is not slowed: the wait
// ends at the first mark.
func ReadyTimeout(shell string) time.Duration {
	if isPowerShell(Kind(shell)) {
		return 20 * time.Second
	}
	return 5 * time.Second
}

// cmdSentinelSuffix prints the marker without a newline (set /p) and reads
// ERRORLEVEL at execution time (!...! under /v:on), after the command ran.
// %PAIRMUX_EOC% then prints core.SentinelBoundary: ConPTY hoists the OSC to the
// start of its render frame, ahead of the output cmd wrote in that frame, but
// the boundary is text and stays behind it. `<nul set /p` leaves ERRORLEVEL at
// 1 (it reads EOF), and builtins such as echo never reset it, so `(call )`
// clears it to 0 once the sentinel has read it; otherwise the next command
// would report this one's stale 1.
const cmdSentinelSuffix = ` & <nul set /p "=%PAIRMUX_ESC%]7779;p;!ERRORLEVEL!%PAIRMUX_BEL%%PAIRMUX_EOC%" & (call )`

// powershellChars spells s as PowerShell [char] subexpressions, for use inside
// a double-quoted string.
func powershellChars(s string) string {
	var b strings.Builder
	for _, r := range s {
		fmt.Fprintf(&b, "$([char]0x%x)", r)
	}
	return b.String()
}

// SentinelSuffix returns the completion marker command for shell. POSIX-like
// shells expose the prior command's status as $?, fish as $status, PowerShell
// as $?/$LASTEXITCODE and cmd.exe as ERRORLEVEL.
func SentinelSuffix(shell string) string {
	switch kind := Kind(shell); {
	case kind == "fish":
		return fishSentinelSuffix
	case isPowerShell(kind):
		return powershellSentinelSuffix
	case kind == "cmd":
		return cmdSentinelSuffix
	}
	return core.SentinelSuffix
}

// cmdSentinelPrefix clears ERRORLEVEL before the command runs. cmd.exe's
// builtins (echo, cd, set, ...) never set it, so the sentinel would otherwise
// report whatever the last program left: one typed with send or by a human, or
// a Ctrl+C'd one. `&` binds loosest in cmd, so the user's && and || still group
// as typed.
const cmdSentinelPrefix = `(call ) & `

// SentinelPrefix returns what is typed before a run command in sentinel mode:
// cmdSentinelPrefix for cmd.exe, "" for every other shell, whose status
// variable is always set by the command itself.
func SentinelPrefix(shell string) string {
	if Kind(shell) == "cmd" {
		return cmdSentinelPrefix
	}
	return ""
}

// SentinelHasBoundary reports whether shell's sentinel suffix prints
// core.SentinelBoundary after the sentinel: the Windows shells, whose output
// passes through ConPTY. Completion detection waits for that boundary only when
// it is coming; a POSIX or fish sentinel is written in order on a Unix pty.
func SentinelHasBoundary(shell string) bool {
	kind := Kind(shell)
	return kind == "cmd" || isPowerShell(kind)
}

// ensureDir creates dir (and parents) and forces 0700 despite umask.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("shellhooks: mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("shellhooks: chmod %s: %w", dir, err)
	}
	return nil
}

// writeShim writes content to path (overwrite: idempotent) and forces 0600.
func writeShim(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("shellhooks: write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("shellhooks: chmod %s: %w", path, err)
	}
	return nil
}
