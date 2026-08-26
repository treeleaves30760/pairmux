package detect

// TTYState is the part of a pane's line discipline that says what the program
// on it is asking for. It is read from the pane's terminal, not from its
// output, which is what makes it independent of wording, language and locale:
// a program that wants a password performs the same two syscalls whether it
// prints "Password:", "密碼：" or nothing at all.
//
// The two flags that matter:
//
//   - ECHO off with ICANON on is the getpass signature. Turning echo off while
//     still asking the kernel for a whole line is what every credential prompt
//     does — sudo, ssh, git, gpg, pinentry-tty, Python's getpass, npm — and
//     nothing else has a reason to. This is the one classification pairmux can
//     make without guessing, and it is the one where guessing wrong is worst.
//   - ICANON off means the program drives raw keystrokes: a shell's line editor
//     (zsh's ZLE, bash's readline), a pager, a full-screen TUI. The terminal
//     says nothing useful about whether such a program wants input, so the text
//     heuristics stay in charge there.
//
// Known is false when the terminal could not be read at all — an older
// terminal recorded before pairmux stored the tty, an unsupported platform, a
// pane whose device has gone. Callers fall back to the text heuristics, which
// is exactly the behaviour that predates this file.
type TTYState struct {
	Echo      bool
	Canonical bool
	Known     bool
}

// Secret reports the getpass signature: a whole line is being read with echo
// suppressed, so whatever is typed must not be seen, echoed or guessed.
func (s TTYState) Secret() bool { return s.Known && s.Canonical && !s.Echo }

// LineOriented reports that the program is reading through the kernel's line
// discipline, so the last line on screen is a prompt in the ordinary sense and
// an answer is a line of text. False for raw-mode programs, where the shape of
// the last line means nothing.
func (s TTYState) LineOriented() bool { return s.Known && s.Canonical }

// RawInput reports that a program has taken the keyboard for itself: it turned
// ICANON off, which is how a pager, an editor, a full-screen TUI or another
// agent's terminal UI arranges to see keystrokes as they are struck.
//
// On its own this says nothing about *when* such a program wants a keystroke —
// that is why LineOriented exists and why the text heuristics stay in charge
// here. What it does establish is intent: a program in this mode reads the
// terminal, so if it is also running no code at all (see FgWait) there is
// nothing left for it to be doing but waiting on the keyboard.
func (s TTYState) RawInput() bool { return s.Known && !s.Canonical }

// ReadTTYState reads the discipline of the terminal at path. It never writes,
// never becomes the controlling terminal, and opens non-blocking, so polling it
// while a program waits on that terminal cannot disturb the program — verified
// against a live getpass prompt. An empty path returns an unknown state.
func ReadTTYState(path string) TTYState {
	if path == "" {
		return TTYState{}
	}
	return readTTYState(path)
}
