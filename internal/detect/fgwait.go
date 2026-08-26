package detect

// FgWait says what the programs holding a pane's terminal are doing right now,
// read from the operating system rather than from anything they printed.
//
// It exists for the case the line discipline and the wording both miss: a
// program that has taken the keyboard for itself — a pager, a TUI, an editor,
// another agent's terminal UI — and is sitting there wanting a keystroke. Such
// a program has ICANON off, which makes TTYState uninformative (see tty.go),
// and its screen says whatever its author chose, which makes the patterns
// uninformative. What is still true is that it is running no code at all.
//
// The distinction this draws is narrow and worth stating plainly: parked means
// nothing on the terminal is on the CPU, not that anything is blocked reading
// the terminal. `sleep 30` is parked. A TUI waiting on a network reply is
// parked. Only in combination with ICANON off — a program that deliberately
// took over the keyboard — does parked become evidence about input, and even
// then it is reported as KindInferred, never as a recognised prompt.
type FgWait int

const (
	// FgUnknown: the terminal's processes could not be inspected — an
	// unsupported platform, a device that has gone, a pane owned by another
	// user. Callers must fall back to the evidence that predates this file.
	FgUnknown FgWait = iota
	// FgParked: every process on the terminal is off the CPU. Whatever they are
	// waiting for, they are not computing.
	FgParked
	// FgWorking: at least one process on the terminal has a thread running.
	// Whatever the screen looks like, this terminal is busy.
	FgWorking
)

func (w FgWait) String() string {
	switch w {
	case FgParked:
		return "parked"
	case FgWorking:
		return "working"
	default:
		return "unknown"
	}
}

// ReadFgWait reports whether the processes attached to the terminal at path are
// parked. It is read-only, opens nothing that could become a controlling
// terminal, and never signals or stops anything, so any number of agents may
// call it on one terminal at once.
//
// An empty path returns FgUnknown, as does any platform without an
// implementation — the caller then behaves exactly as it did before this
// existed.
func ReadFgWait(path string) FgWait {
	if path == "" {
		return FgUnknown
	}
	return readFgWait(path)
}
