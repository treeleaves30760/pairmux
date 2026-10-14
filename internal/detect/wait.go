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
// EndOffset is one past the mark (where a later scan resumes); for a bounded
// sentinel that mark is the boundary, and ExitCode the sentinel's. On timeout,
// MarkStart is -1, ExitCode is -1, and EndOffset is how far the stream was
// scanned.
type RunResult struct {
	Outcome   WaitOutcome
	ExitCode  int
	MarkStart int64
	EndOffset int64
	Prompt    Prompt
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

// graceBoundary is how long a bounded sentinel waits for its boundary before
// the sentinel alone ends the command. The boundary is written by the same
// call as the sentinel and normally lands in the same read; the grace covers a
// frame that ConPTY flushes late, and a shell that never prints one.
const graceBoundary = 500 * time.Millisecond

// boundedSentinel tracks a sentinel whose shell prints core.SentinelBoundary
// after it (cmd.exe, PowerShell — see shellhooks.SentinelHasBoundary). ConPTY
// emits the sentinel OSC at the start of its render frame, ahead of the
// command output written in that same frame, so the sentinel's own offset can
// fall before the output. The boundary is text and keeps its order, so the
// command ends at the first boundary after the sentinel, with the sentinel's
// exit code. The hoisted OSC then sits inside the output region, where
// shape.StripANSI removes it.
type boundedSentinel struct {
	held       *Mark
	graceUntil time.Time
}

// observe consumes one mark at/after the baseline. It returns the completion
// once a held sentinel meets its boundary.
func (b *boundedSentinel) observe(m Mark) (RunResult, bool) {
	switch {
	case b.held == nil && m.Kind == MarkSentinel:
		s := m
		b.held, b.graceUntil = &s, time.Now().Add(graceBoundary)
	case b.held != nil && m.Kind == MarkBoundary && m.Start >= b.held.End:
		return RunResult{Outcome: OutcomeDone, ExitCode: b.held.ExitCode, MarkStart: m.Start, EndOffset: m.End}, true
	}
	return RunResult{}, false
}

// expired reports the fallback completion at the held sentinel once the grace
// has run out with no boundary, or once force is set (the caller's deadline).
func (b *boundedSentinel) expired(now time.Time, force bool) (RunResult, bool) {
	if b.held == nil || (!force && now.Before(b.graceUntil)) {
		return RunResult{}, false
	}
	return doneResult(*b.held), true
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
	bounded  *boundedSentinel // sentinel mode, for a shell that prints the boundary
}

// NewCompletionWatcher returns a watcher for a command whose output begins at
// offset from. Hooks mode correlates on C; sentinel mode takes the first
// sentinel mark, which carries no such ambiguity. bounded, meaningful only in
// sentinel mode, says the shell prints core.SentinelBoundary after its sentinel
// (shellhooks.SentinelHasBoundary); the command then ends at that boundary.
func NewCompletionWatcher(from int64, mode core.Mode, bounded bool) *CompletionWatcher {
	w := &CompletionWatcher{
		from: from, off: from, sc: NewScanner(from),
		want: completionKind(mode), requireC: mode == core.ModeHooks,
	}
	if bounded && mode == core.ModeSentinel {
		w.bounded = &boundedSentinel{}
	}
	return w
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
				if w.bounded != nil {
					if res, ok := w.bounded.observe(m); ok {
						return res, true, nil
					}
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
	if w.bounded != nil {
		if res, ok := w.bounded.expired(time.Now(), false); ok {
			return res, true, nil
		}
	}
	return RunResult{}, false, nil
}

// WaitCompletion polls raw.log for the completion mark of a command sent at
// offset from. It keeps one persistent Scanner across polls so a mark split
// across appends is still recognized. It returns on the first matching mark
// with Start >= from, or on timeout. bounded is as for NewCompletionWatcher: a
// bounded sentinel completes at the boundary that follows it, or at the
// sentinel itself once graceBoundary or the timeout has passed without one.
func WaitCompletion(j *journal.Journal, from int64, mode core.Mode, bounded bool, timeout, poll time.Duration) (RunResult, error) {
	return waitCompletion(j, from, mode, bounded, timeout, poll, false, "")
}

// WaitCommand is WaitCompletion with prompt-aware early return. It is used by
// `pairmux run`: once a quiet terminal is judged to be waiting for input, it
// returns OutcomeAwaitingInput instead of making the caller sit out the overall
// command timeout. tty is the pane's terminal device, which lets that judgement
// rest on the line discipline rather than on the wording of the prompt; "" is
// accepted and falls back to wording.
func WaitCommand(j *journal.Journal, from int64, mode core.Mode, bounded bool, timeout, poll time.Duration, tty string) (RunResult, error) {
	return waitCompletion(j, from, mode, bounded, timeout, poll, true, tty)
}

func waitCompletion(j *journal.Journal, from int64, mode core.Mode, bounded bool, timeout, poll time.Duration, stopOnPrompt bool, tty string) (RunResult, error) {
	if poll <= 0 {
		poll = defaultPoll
	}
	want := completionKind(mode)
	deadline := time.Now().Add(timeout)
	var bs *boundedSentinel
	if bounded && mode == core.ModeSentinel {
		bs = &boundedSentinel{}
	}

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
					if m.Start < from {
						continue
					}
					if bs != nil {
						if res, ok := bs.observe(m); ok {
							return res, nil
						}
						continue
					}
					if m.Kind == want {
						return doneResult(m), nil
					}
				}
			}
		}
		now := time.Now()
		pastDeadline := !now.Before(deadline)
		if bs != nil {
			// A completion was observed; it beats both a prompt and a timeout.
			if res, ok := bs.expired(now, pastDeadline); ok {
				return res, nil
			}
		}
		if stopOnPrompt && (bs == nil || bs.held == nil) {
			if status, prompt := Classify(j, core.StatusRunning, mode, tty); status == core.StatusAwaitingInput {
				return RunResult{
					Outcome: OutcomeAwaitingInput, ExitCode: -1, MarkStart: -1,
					EndOffset: off, Prompt: prompt,
				}, nil
			}
		}
		if pastDeadline {
			return RunResult{Outcome: OutcomeTimeout, ExitCode: -1, MarkStart: -1, EndOffset: off}, nil
		}
		// Wake for the grace expiry rather than oversleeping it by a poll.
		sleep := poll
		if bs != nil && bs.held != nil {
			if rem := time.Until(bs.graceUntil); rem < sleep {
				sleep = rem
			}
		}
		if sleep < time.Millisecond {
			sleep = time.Millisecond
		}
		time.Sleep(sleep)
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

// WaitPromptCycle waits, after WaitReady saw a prompt, for the prompt cycle
// that a nudge (an Enter sent to a new hooks terminal) produces to finish: a
// D mark followed by an A, or simply a second A (fish may mark an empty line
// with A alone). It reports whether the cycle was seen before timeout.
//
// Without it the nudge's own completion mark can land after the first `run`
// has taken its send offset, and that run then reports "done" for a command
// that has not yet executed. Shells that start slowly — PowerShell under
// ConPTY, or any shell with a heavy profile — hit this reliably.
func WaitPromptCycle(j *journal.Journal, timeout time.Duration) (bool, error) {
	poll := 25 * time.Millisecond
	deadline := time.Now().Add(timeout)
	sc := NewScanner(0)
	off := int64(0)
	prompts, sawD := 0, false
	for {
		if size := j.Size(); size > off {
			data, err := j.ReadRange(off, size)
			if err != nil {
				return false, fmt.Errorf("detect: wait prompt cycle: %w", err)
			}
			off += int64(len(data))
			for _, m := range sc.Feed(data) {
				switch m.Kind {
				case MarkD:
					sawD = true
				case MarkA:
					prompts++
					if sawD || prompts >= 2 {
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
