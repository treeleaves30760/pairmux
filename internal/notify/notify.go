// Package notify fires a best-effort desktop notification so a human pairing
// with an agent can be summoned to a terminal. Callers treat failures as
// non-fatal: a notification is a convenience, never a dependency.
package notify

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"
)

// Notify shows a desktop notification: osascript on macOS, notify-send on
// Linux, a Windows PowerShell notification-area balloon (shown as a toast on
// Windows 10/11) on Windows. On other platforms, or when the helper binary is
// missing, it returns an error the caller may ignore.
func Notify(title, body string) error {
	argv, err := argvFor(runtime.GOOS, title, body)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("notify: %s not found: %w", argv[0], err)
	}
	if runtime.GOOS == "windows" {
		// The balloon lives only as long as its PowerShell process, which
		// therefore lingers for a few seconds; do not make the caller wait.
		cmd := exec.Command(bin, argv[1:]...)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("notify: %s failed: %w", argv[0], err)
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	out, err := exec.Command(bin, argv[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("notify: %s failed: %w: %s", argv[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// argvFor builds the notification argv for a GOOS. Pure, so tests cover every
// platform branch without firing a notification.
func argvFor(goos, title, body string) ([]string, error) {
	switch goos {
	case "darwin":
		script := `display notification "` + appleScriptArg(body) + `" with title "` + appleScriptArg(title) + `"`
		return []string{"osascript", "-e", script}, nil
	case "linux":
		return []string{"notify-send", title, body}, nil
	case "windows":
		script := "Add-Type -AssemblyName System.Windows.Forms\n" +
			"$n = New-Object System.Windows.Forms.NotifyIcon\n" +
			"$n.Icon = [System.Drawing.SystemIcons]::Information\n" +
			"$n.BalloonTipTitle = " + powershellArg(title) + "\n" +
			"$n.BalloonTipText = " + powershellArg(body) + "\n" +
			"$n.Visible = $true\n" +
			"$n.ShowBalloonTip(10000)\n" +
			"Start-Sleep -Seconds 8\n" +
			"$n.Dispose()\n"
		return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
			"-EncodedCommand", encodePowerShell(script)}, nil
	default:
		return nil, fmt.Errorf("notify: unsupported platform %q", goos)
	}
}

// appleScriptArg prepares s for embedding in a double-quoted AppleScript
// string literal: escape backslashes then double quotes, and flatten newlines
// (an AppleScript literal cannot span lines) to spaces.
func appleScriptArg(s string) string {
	return escapeAppleScript(flattenNewlines(s))
}

// escapeAppleScript escapes backslashes and double quotes for an AppleScript
// string literal. Backslash first, or the quote escapes would be re-escaped.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// flattenNewlines replaces CR/LF with spaces.
func flattenNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// powershellArg renders s as a PowerShell single-quoted string literal, in
// which only the quote itself needs escaping (by doubling).
func powershellArg(s string) string {
	return "'" + strings.ReplaceAll(flattenNewlines(s), "'", "''") + "'"
}

// encodePowerShell renders a script for -EncodedCommand (base64 UTF-16LE), so
// no command-line quoting is involved at all.
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		b[2*i] = byte(u)
		b[2*i+1] = byte(u >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}
