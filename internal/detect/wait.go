package detect

import (
	"fmt"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
)

// WaitOutcome is the terminal state of a wait.
type WaitOutcome int

const (
	OutcomeDone          WaitOutcome = iota // the awaited mark arrived
	OutcomeTimeout                          // the deadline elapsed first
	OutcomeAwaitingInput                    // a quiet interactive prompt appeared
)

// RunResult reports the result of WaitCompletion. On OutcomeDone, MarkStart is
// the offset of the completion mark (output region is [from, MarkStart)) and
// EndOffset is one past the mark (where a later scan resumes). On timeout,
// MarkStart is -1, ExitCode is -1, and EndOffset is how far the stream was
// scanned.
type RunResult struct {
	Outcome   WaitOutcome
	ExitCode  int
	MarkStart int64
	EndOffset int64
	Prompt    string
}

const defaultPoll = 100 * time.Millisecond

// completionKind is the mark that ends a command in the given mode.
func completionKind(mode core.Mode) MarkKind {
	if mode == core.ModeSentinel {
		return MarkSentinel
	}
	return MarkD
}

// doneResult renders a completion mark as a finished RunResult.
func doneResult(m Mark) RunResult {
	return RunResult{Outcome: OutcomeDone, ExitCode: m.ExitCode, MarkStart: m.Start, EndOffset: m.End}
}

// CompletionWatcher reports a command's completion to a caller that cannot
// block on it. WaitCompletion owns its own loop, which suits `run` (it has
// nothing else to watch); `wait` must poll a completion alongside notes,
// patterns and quiescence, so it drives a watcher from its own loop instead.
//
// State — the Scanner, the C-correlation flag, a held D — lives across polls,
// so a mark split across two appends is still recognized and every byte is
// scanned exactly once no matter how long the wait runs.
//
// Correlation matches hooks-mode `run` (see waitCompletionCorrelated): the
// completion is the first D after the first C at/after the baseline, so a
// command a human types into the same pane cannot spoof it. A D with no
// preceding C is held for graceNoC and then accepted — that is how bash 3.2,
// which emits no C at all, completes.
type CompletionWatcher struct {
	from     int64
	off      int64
	sc       *Scanner
	want     MarkKind
	requireC bool
	seenC    bool
	heldD    *Mark
	graceEnd time.Time
}

// NewCompletionWatcher returns a watcher for a command whose output begins at
// offset from. Hooks mode correlates on C; sentinel mode takes the first
// sentinel mark, which carries no such ambiguity.
func NewCompletionWatcher(from int64, mode core.Mode) *CompletionWatcher {
	return &CompletionWatcher{
		from: from, off: from, sc: NewScanner(from),
		want: completionKind(mode), requireC: mode == core.ModeHooks,
	}
}

// Poll consumes whatever raw.log has gained since the previous call and reports
// the completion once it is established. done stays false while the command is
// still running, including while a C-less D sits in its grace window.
func (w *CompletionWatcher) Poll(j *journal.Journal) (res RunResult, done bool, err error) {
	if size := j.Size(); size > w.off {
		data, err := j.ReadRange(w.off, size)
		if err != nil {
			return RunResult{}, false, fmt.Errorf("detect: completion watcher: %w", err)
		}
		if len(data) > 0 {
			w.off += int64(len(data))
			for _, m := range w.sc.Feed(data) {
				if m.Start < w.from {
					continue
				}
				if !w.requireC {
					if m.Kind == w.want {
						return doneResult(m), true, nil
					}
					continue
				}
				switch m.Kind {
				case MarkC:
					if !w.seenC {
						w.seenC = true
						w.heldD = nil // a D before our C belonged to someone else
					}
				case MarkD:
					if w.seenC {
						return doneResult(m), true, nil
					}
					if w.heldD == nil {
						d := m
						w.heldD, w.graceEnd = &d, time.Now().Add(graceNoC)
					}
				}
			}
		}
	}
	if w.heldD != nil && !time.Now().Before(w.graceEnd) {
		return doneResult(*w.heldD), true, nil
	}
	return RunResult{}, false, nil
}

// WaitCompletion polls raw.log for the completion mark of a command sent at
// offset from. It keeps one persistent Scanner across polls so a mark split
// across appends is still recognized. It returns on the first matching mark
// with Start >= from, or on timeout.
func WaitCompletion(j *journal.Journal, from int64, mode core.Mode, timeout, poll time.Duration) (RunResult, error) {
	return waitCompletion(j, from, mode, timeout, poll, false)
}

// WaitCommand is WaitCompletion with prompt-aware early return. It is used by
// `pairmux run`: after output has been quiet long enough for Refine to trust a
// prompt-shaped last line, it returns OutcomeAwaitingInput instead of making
// the caller wait for the overall command timeout.
func WaitCommand(j *journal.Journal, from int64, mode core.Mode, timeout, poll time.Duration) (RunResult, error) {
	return waitCompletion(j, from, mode, timeout, poll, true)
}

func waitCompletion(j *journal.Journal, from int64, mode core.Mode, timeout, poll time.Duration, stopOnPrompt bool) (RunResult, error) {
	if poll <= 0 {
		poll = defaultPoll
	}
	want := completionKind(mode)
	deadline := time.Now().Add(timeout)

	sc := NewScanner(from)
	off := from
	for {
		if size := j.Size(); size > off {
			data, err := j.ReadRange(off, size)
			if err != nil {
				return RunResult{}, fmt.Errorf("detect: wait completion: %w", err)
			}
			if len(data) > 0 {
				off += int64(len(data))
				for _, m := range sc.Feed(data) {
					if m.Kind == want && m.Start >= from {
						return RunResult{
							Outcome:   OutcomeDone,
							ExitCode:  m.ExitCode,
							MarkStart: m.Start,
							EndOffset: m.End,
						}, nil
					}
				}
			}
		}
		if stopOnPrompt {
			if status, prompt := Refine(j, core.StatusRunning, mode); status == core.StatusAwaitingInput {
				return RunResult{
					Outcome: OutcomeAwaitingInput, ExitCode: -1, MarkStart: -1,
					EndOffset: off, Prompt: prompt,
				}, nil
			}
		}
		if !time.Now().Before(deadline) {
			return RunResult{Outcome: OutcomeTimeout, ExitCode: -1, MarkStart: -1, EndOffset: off}, nil
		}
		time.Sleep(poll)
	}
}

// WaitReady waits for a freshly created terminal's shell to be interactive.
//
// In hooks mode it waits for the first MarkA (prompt start); if none arrives
// before timeout it returns (false, nil) so the caller can degrade to sentinel
// mode. In sentinel mode it waits for the stream to go quiet for 800ms and
// always reports (false, nil) — there are no hooks to confirm.
func WaitReady(j *journal.Journal, mode core.Mode, timeout time.Duration) (gotHooks bool, err error) {
	if mode == core.ModeSentinel {
		if _, err := Quiesce(j, 800*time.Millisecond, timeout); err != nil {
			return false, err
		}
		return false, nil
	}

	poll := 50 * time.Millisecond
	deadline := time.Now().Add(timeout)
	sc := NewScanner(0)
	off := int64(0)
	for {
		if size := j.Size(); size > off {
			data, err := j.ReadRange(off, size)
			if err != nil {
				return false, fmt.Errorf("detect: wait ready: %w", err)
			}
			if len(data) > 0 {
				off += int64(len(data))
				for _, m := range sc.Feed(data) {
					if m.Kind == MarkA {
						return true, nil
					}
				}
			}
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		time.Sleep(poll)
	}
}

// Quiesce blocks until raw.log's size is unchanged for idle, or until timeout
// elapses. quiet is true when the idle period was reached, false on timeout.
func Quiesce(j *journal.Journal, idle, timeout time.Duration) (quiet bool, err error) {
	poll := idle / 4
	if poll > 50*time.Millisecond {
		poll = 50 * time.Millisecond
	}
	if poll < time.Millisecond {
		poll = time.Millisecond
	}

	deadline := time.Now().Add(timeout)
	lastSize := j.Size()
	lastChange := time.Now()
	for {
		time.Sleep(poll)
		now := time.Now()
		if size := j.Size(); size != lastSize {
			lastSize = size
			lastChange = now
		}
		if now.Sub(lastChange) >= idle {
			return true, nil
		}
		if !now.Before(deadline) {
			return false, nil
		}
	}
}

// DeriveStatus computes a terminal's status without writing anything (read-only;
// it never settles events). alive comes from the tmux pane liveness check.
func DeriveStatus(j *journal.Journal, alive bool, mode core.Mode) core.Status {
	if !alive {
		return core.StatusDead
	}

	pending, ok, err := j.PendingCmd()
	if err != nil {
		return core.StatusUnknown
	}
	if ok {
		// A command was started but no completion event was recorded. Scan the
		// stream from its send offset: if the completion mark is already there,
		// the command has finished (settlement is merely pending) -> idle.
		if scanForMark(j, pending.Offset, completionKind(mode)) {
			return core.StatusIdle
		}
		return core.StatusRunning
	}

	// No in-flight command. Hooks give us clean boundaries, so idle. Sentinel
	// mode has no prompt signal, so fall back to recent-activity heuristics.
	if mode == core.ModeSentinel {
		if mt, ok := j.LastModified(); ok && time.Since(mt) < time.Second {
			return core.StatusUnknown
		}
	}
	return core.StatusIdle
}

// scanForMark reports whether a mark of kind want appears in raw.log at or
// after offset from. It is read-only.
func scanForMark(j *journal.Journal, from int64, want MarkKind) bool {
	data, err := j.ReadRange(from, -1)
	if err != nil {
		return false
	}
	sc := NewScanner(from)
	for _, m := range sc.Feed(data) {
		if m.Kind == want && m.Start >= from {
			return true
		}
	}
	return false
}
