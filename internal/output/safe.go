package output

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Text that reaches a person's terminal through pairmux is not all pairmux's
// own. Notes, command lines and event text are written by one party — an agent,
// a sub-agent reporting upward, whatever drove a terminal — and read later by
// another, usually the human supervising them. JSON mode is safe by
// construction (encoding/json escapes every control character), but the human
// block, the watch dashboard and the TUI write strings to the terminal as they
// are. A stored ESC would then be executed by that terminal rather than shown:
// it can rewrite or erase the lines around it (so the activity log no longer
// says what was run), set the window title, or load the clipboard (OSC 52).
//
// Printable and PrintableLine close that at the point of display. They never
// delete: a neutralised character is shown as a visible escape (ESC becomes
// the four characters \x1b), so the reader still sees that something was there.

// Printable returns s safe to write to a terminal as text. Line feeds and tabs
// are kept. Every other C0 control, DEL, the C1 controls and bytes that are not
// valid UTF-8 are replaced by a visible escape. A string with none of these is
// returned unchanged. Shaped program output (shape.StripANSI) has already lost
// its escape sequences and C0 controls, so for it this only catches what that
// pass leaves in: DEL, C1 controls and bytes that are not UTF-8.
func Printable(s string) string { return printable(s, false) }

// PrintableLine is Printable for text that must stay on one row (a table cell,
// a TUI line): a line feed is escaped as well and a tab becomes a space.
func PrintableLine(s string) string { return printable(s, true) }

func printable(s string, oneLine bool) string {
	i := firstUnsafe(s, oneLine)
	if i < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	b.WriteString(s[:i])
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			// Not UTF-8. Shown as the byte it is: a terminal in an 8-bit mode
			// would read 0x9b as CSI.
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case oneLine && r == '\t':
			b.WriteByte(' ')
		case !unsafeRune(r, oneLine):
			b.WriteString(s[i : i+size])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		default:
			fmt.Fprintf(&b, `\x%02x`, r)
		}
		i += size
	}
	return b.String()
}

// firstUnsafe returns the byte offset of the first character printable would
// rewrite, or -1 when s can be returned as it is.
func firstUnsafe(s string, oneLine bool) int {
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if unsafeRune(rune(c), oneLine) || oneLine && c == '\t' {
				return i
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 || unsafeRune(r, oneLine) {
			return i
		}
		i += size
	}
	return -1
}

func unsafeRune(r rune, oneLine bool) bool {
	switch {
	case r == '\n':
		return oneLine
	case r == '\t':
		return false
	case r < 0x20, r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f: // C1, including the 8-bit CSI/OSC/DCS
		return true
	}
	return false
}
