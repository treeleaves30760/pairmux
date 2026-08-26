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
// the ones where the layers disagree: a credential prompt nobody has a pattern
// for must still be refused, a raw-mode program must not have its echo state
// read as a password prompt, and a terminal with a thread on the CPU must not
// be called a question however its last line looks.
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
		fg           FgWait
		want         PromptKind
	}{
		{"echo off settles a prompt no pattern knows", getpass, unknown, false, refineQuiet, true, FgParked, KindSecret},
		{"echo off outranks open-looking wording", getpass, openWord, true, refineQuiet, true, FgParked, KindSecret},
		// The discipline is definitive, so it holds even against the kernel:
		// a getpass whose program has a thread spinning is still a getpass.
		{"echo off outranks a working terminal", getpass, unknown, false, refineQuiet, true, FgWorking, KindSecret},
		{"recognised secret wording without the terminal", unreadable, secretWord, true, refineQuiet, true, FgUnknown, KindSecret},
		{"recognised open prompt stays answerable", plainRead, openWord, true, refineQuiet, true, FgParked, KindOpen},
		{"unrecognised wording, silent long enough", plainRead, unknown, false, inferQuiet, true, FgUnknown, KindInferred},
		{"the same, not yet silent enough", plainRead, unknown, false, inferQuiet - time.Millisecond, true, FgUnknown, KindNone},
		{"a finished line is not a question", plainRead, "", false, inferQuiet, false, FgUnknown, KindNone},
		// The kernel vetoes the weakest branch's worst false positive: a command
		// that printed "Building... " and went to work looks exactly like a
		// question until you ask whether it is running anything.
		{"a working terminal is not asking, whatever it printed", plainRead, unknown, false, inferQuiet, true, FgWorking, KindNone},
		{"a parked terminal still infers", plainRead, unknown, false, inferQuiet, true, FgParked, KindInferred},
		// A pager sits in raw mode with (END) on screen: the terminal cannot
		// speak for its timing, so the pattern answers first and immediately.
		{"raw mode defers to the pattern", rawMode, "file (END)", true, refineQuiet, true, FgParked, KindOpen},
		// Raw mode plus idleness is the case wording could never reach: a TUI,
		// an editor, an ssh session at a remote prompt, another agent's UI.
		{"raw mode infers once the program stops running", rawMode, unknown, false, rawQuiet, true, FgParked, KindInferred},
		{"raw mode infers with no prompt line at all", rawMode, "", false, rawQuiet, false, FgParked, KindInferred},
		{"raw mode waits out rawQuiet first", rawMode, unknown, false, rawQuiet - time.Millisecond, true, FgParked, KindNone},
		{"a busy TUI is not a question", rawMode, unknown, false, rawQuiet, true, FgWorking, KindNone},
		// Without the kernel there is nothing to promote raw mode with, so the
		// behaviour is exactly what it was before FgWait existed.
		{"raw mode never infers unasked", rawMode, unknown, false, inferQuiet, true, FgUnknown, KindNone},
		{"unreadable terminal never infers", unreadable, unknown, false, inferQuiet, true, FgParked, KindNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.disc, tc.line, tc.recognized, tc.quiet, tc.unterminated, func() FgWait { return tc.fg })
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

// TestClassifyDefersProcessScan pins the cost: the two layers that can decide
// from the terminal alone must never pay for a walk of its processes, and the
// two that can consult it must agree on one reading rather than taking two.
func TestClassifyDefersProcessScan(t *testing.T) {
	getpass := TTYState{Canonical: true, Known: true}
	plainRead := TTYState{Echo: true, Canonical: true, Known: true}
	rawMode := TTYState{Known: true}

	tests := []struct {
		name       string
		disc       TTYState
		recognized bool
		wantCalls  int
	}{
		{"the discipline settles it alone", getpass, false, 0},
		{"the pattern settles it alone", plainRead, true, 0},
		{"raw mode asks once", rawMode, false, 1},
		{"a declined raw branch does not ask twice", plainRead, false, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			classify(tc.disc, "Widget:", tc.recognized, inferQuiet, true, func() FgWait {
				calls++
				return FgUnknown
			})
			if calls != tc.wantCalls {
				t.Fatalf("fg consulted %d times, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// TestOnceFgWaitMemoizes pins that the memo, not the caller, is what keeps two
// branches from reading a moving terminal twice.
func TestOnceFgWaitMemoizes(t *testing.T) {
	fg := onceFgWait("") // an empty path always answers FgUnknown
	first := fg()
	for i := 0; i < 3; i++ {
		if got := fg(); got != first {
			t.Fatalf("call %d = %v, want the memoized %v", i+2, got, first)
		}
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
