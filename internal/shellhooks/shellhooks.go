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
// treats ESC as "clear the line".
var cmdEnv = map[string]string{"PAIRMUX_ESC": "\x1b", "PAIRMUX_BEL": "\x07"}

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
// the prompt hook does: 0 on success, else the native exit code, else 1.
const powershellSentinelSuffix = `; $__pmx_ok = $?; [Console]::Write("$([char]27)]7779;p;$(if ($__pmx_ok) {0} elseif ($LASTEXITCODE) {$LASTEXITCODE} else {1})$([char]7)")`

// cmdSentinelSuffix prints the marker without a newline (set /p) and reads
// ERRORLEVEL at execution time (!...! under /v:on), after the command ran.
const cmdSentinelSuffix = ` & <nul set /p "=%PAIRMUX_ESC%]7779;p;!ERRORLEVEL!%PAIRMUX_BEL%"`

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
