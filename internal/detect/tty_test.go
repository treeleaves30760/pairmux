package detect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTTYStateMeaning pins the two flags to the states they are read from,
// recorded against a real terminal: idle zsh is -echo -icanon (its line editor
// owns the keystrokes), a plain `read` is echo icanon, getpass is -echo icanon,
// vim is -echo -icanon, and a running command that is not reading anything is
// echo icanon.
func TestTTYStateMeaning(t *testing.T) {
	tests := []struct {
		name             string
		s                TTYState
		secret, lineWise bool
	}{
		{"getpass: echo off, whole line still wanted", TTYState{Canonical: true, Known: true}, true, true},
		{"plain read: echo on, whole line", TTYState{Echo: true, Canonical: true, Known: true}, false, true},
		{"idle shell line editor: raw", TTYState{Known: true}, false, false},
		{"full-screen TUI: raw", TTYState{Known: true}, false, false},
		{"unreadable terminal claims nothing", TTYState{}, false, false},
		// Known must gate both: an unread terminal is all-zero, which would
		// otherwise read as the getpass signature and forbid every answer.
		{"unknown is not mistaken for echo-off", TTYState{Canonical: true}, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Secret(); got != tc.secret {
				t.Errorf("Secret() = %v, want %v", got, tc.secret)
			}
			if got := tc.s.LineOriented(); got != tc.lineWise {
				t.Errorf("LineOriented() = %v, want %v", got, tc.lineWise)
			}
		})
	}
}

func TestReadTTYStateOnNonTerminals(t *testing.T) {
	regular := filepath.Join(t.TempDir(), "not-a-tty")
	if err := os.WriteFile(regular, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", regular, filepath.Join(t.TempDir(), "missing")} {
		if got := ReadTTYState(path); got.Known {
			t.Fatalf("ReadTTYState(%q).Known = true, want false", path)
		}
	}
}

// TestClassifyLayers walks the evidence ladder. The cases that matter most are
// the ones where the terminal and the wording disagree: a credential prompt
// nobody has a pattern for must still be refused, and a raw-mode program must
// not have its echo state read as a password prompt.
func TestClassifyLayers(t *testing.T) {
	const (
		secretWord = "Password:"
		openWord   = "Continue? [y/N]"
		unknown    = "Widget:"
	)
	getpass := TTYState{Canonical: true, Known: true}
	plainRead := TTYState{Echo: true, Canonical: true, Known: true}
	rawMode := TTYState{Known: true}
	unreadable := TTYState{}

	tests := []struct {
		name         string
		disc         TTYState
		line         string
		recognized   bool
		quiet        time.Duration
		unterminated bool
		want         PromptKind
	}{
		{"echo off settles a prompt no pattern knows", getpass, unknown, false, refineQuiet, true, KindSecret},
		{"echo off outranks open-looking wording", getpass, openWord, true, refineQuiet, true, KindSecret},
		{"recognised secret wording without the terminal", unreadable, secretWord, true, refineQuiet, true, KindSecret},
		{"recognised open prompt stays answerable", plainRead, openWord, true, refineQuiet, true, KindOpen},
		{"unrecognised wording, silent long enough", plainRead, unknown, false, inferQuiet, true, KindInferred},
		{"the same, not yet silent enough", plainRead, unknown, false, inferQuiet - time.Millisecond, true, KindNone},
		{"a finished line is not a question", plainRead, "", false, inferQuiet, false, KindNone},
		// A pager sits in raw mode with (END) on screen: the terminal cannot
		// speak for it, so the pattern must, and inference must not fire.
		{"raw mode defers to the pattern", rawMode, "file (END)", true, refineQuiet, true, KindOpen},
		{"raw mode never infers", rawMode, unknown, false, inferQuiet, true, KindNone},
		{"unreadable terminal never infers", unreadable, unknown, false, inferQuiet, true, KindNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.disc, tc.line, tc.recognized, tc.quiet, tc.unterminated)
			if got.Kind != tc.want {
				t.Fatalf("kind = %v, want %v", got.Kind, tc.want)
			}
			if got.Waiting() != (tc.want != KindNone) {
				t.Fatalf("Waiting() = %v for kind %v", got.Waiting(), got.Kind)
			}
			if got.Secret() != (tc.want == KindSecret) {
				t.Fatalf("Secret() = %v for kind %v", got.Secret(), got.Kind)
			}
		})
	}
}

func TestUnterminatedTail(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"prompt leaves the line open", "$ sudo -v\nPassword: ", true},
		{"finished output closes it", "$ make\nbuilt\n", false},
		{"carriage return counts as closed", "progress\r", false},
		{"ANSI colour after the text still counts as open", "Enter: \x1b[0m", true},
		{"empty journal says nothing", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := mustOpen(t)
			writeRawFile(t, j, []byte(tc.raw))
			if got := unterminatedTail(j); got != tc.want {
				t.Fatalf("unterminatedTail = %v, want %v (raw %q)", got, tc.want, tc.raw)
			}
		})
	}
}
