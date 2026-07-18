// Package notify fires a best-effort desktop notification so a human pairing
// with an agent can be summoned to a terminal. Callers treat failures as
// non-fatal: a notification is a convenience, never a dependency.
package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Notify shows a desktop notification: osascript on macOS, notify-send on
// Linux. On other platforms, or when the helper binary is missing, it returns
// an error the caller may ignore.
func Notify(title, body string) error {
	argv, err := argvFor(runtime.GOOS, title, body)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("notify: %s not found: %w", argv[0], err)
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
