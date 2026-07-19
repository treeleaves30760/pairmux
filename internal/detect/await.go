package detect

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/shape"
)

const (
	// refineQuiet is how long the journal must be untouched before a running
	// command's last line is trusted as an interactive prompt (a busy command
	// may print prompt-looking text and immediately keep going).
	refineQuiet = 800 * time.Millisecond

	// refineTail bounds how much of the journal tail is shaped for prompt
	// detection; a prompt is always within the last few lines.
	refineTail = 4096

	// graceNoC is how long a MarkD with no preceding MarkC is held before being
	// accepted as the completion anyway (bash 3.2 hooks emit D but never C).
	graceNoC = 250 * time.Millisecond
)

// Interactive-prompt heuristics, compiled once. Lines are matched after
// CollapseCR+StripANSI shaping and whitespace trimming. The pager rule covers
// less, which renders its status line as "<filename> (END)" (suffix, not
// prefix) and shows a bare ":" while mid-file, and more, whose "--More--(n%)"
// can carry surrounding text — hence: --More-- anywhere, "(END)" as a line
// suffix, or a line that is exactly ":".
var (
	reSecretPrompt = regexp.MustCompile(`(?i)\bpass(?:word|phrase|code)\b.*:\s*$`)
	reConfirmYN    = regexp.MustCompile(`\[[yY]/[nN]\]\s*\??\s*$`)
	reConfirmWord  = regexp.MustCompile(`\((?:yes/no|y/n)\)\s*\??\s*$`)
	rePager        = regexp.MustCompile(`--More--|\(END\)\s*$|^:\s*$`)
	rePressKey     = regexp.MustCompile(`(?i)press (any key|enter|return)`)
)

// isInteractivePrompt reports whether a shaped, trimmed line looks like a
// program waiting for a human answer.
func isInteractivePrompt(line string) bool {
	return strings.TrimSpace(line) == ">>>" ||
		reSecretPrompt.MatchString(line) ||
		reConfirmYN.MatchString(line) ||
		reConfirmWord.MatchString(line) ||
		rePager.MatchString(line) ||
		rePressKey.MatchString(line)
}

// LooksSecret reports whether prompt belongs to the password/passphrase/
// passcode class, i.e. an answer to it must never be echoed or logged.
func LooksSecret(prompt string) bool {
	return reSecretPrompt.MatchString(strings.TrimSpace(prompt))
}

// Refine upgrades StatusRunning to StatusAwaitingInput when the journal has
// gone quiet and its last non-blank line looks like an interactive prompt.
// It returns the prompt line on upgrade, "" otherwise. Refine is cheap and
// non-blocking (a single mtime check, no polling) and read-only. mode is
// accepted for signature stability; the heuristics apply identically in both
// modes.
func Refine(j *journal.Journal, st core.Status, mode core.Mode) (core.Status, string) {
	if st != core.StatusRunning {
		return st, ""
	}
	mt, ok := j.LastModified()
	if !ok || time.Since(mt) < refineQuiet {
		return st, ""
	}
	data, _, err := j.TailBytes(refineTail)
	if err != nil || len(data) == 0 {
		return st, ""
	}
	shaped := shape.StripANSI(shape.CollapseCR(data))
	line := lastNonBlankLine(string(shaped))
	if line == "" || !isInteractivePrompt(line) {
		return st, ""
	}
	return core.StatusAwaitingInput, line
}

// lastNonBlankLine returns the trimmed final line of s that has any
// non-whitespace content ("" when there is none).
func lastNonBlankLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// WaitCompletionCorrelated is hooks-mode completion hardened against a human
// typing commands into the same pane: the completion mark is the first MarkD
// that appears after the first MarkC found at/after from, so a stray D from a
// human-run command (which precedes our command's C) is not mistaken for our
// completion.
//
// With requireC false it behaves exactly like WaitCompletion in hooks mode.
// With requireC true, a D arriving before any C is held for a 250ms grace
// window: if a C still hasn't appeared, the held D is accepted (bash 3.2 hooks
// emit no C at all). A held D is also accepted when the overall deadline
// expires — a completion was observed, which beats reporting a timeout.
func WaitCompletionCorrelated(j *journal.Journal, from int64, timeout, poll time.Duration, requireC bool) (RunResult, error) {
	return waitCompletionCorrelated(j, from, timeout, poll, requireC, false)
}

// WaitCommandCorrelated is the prompt-aware form used by hooks-mode `run`.
// Completion correlation retains priority over the heuristic prompt outcome.
func WaitCommandCorrelated(j *journal.Journal, from int64, timeout, poll time.Duration, requireC bool) (RunResult, error) {
	return waitCompletionCorrelated(j, from, timeout, poll, requireC, true)
}

func waitCompletionCorrelated(j *journal.Journal, from int64, timeout, poll time.Duration, requireC, stopOnPrompt bool) (RunResult, error) {
	if !requireC {
		return waitCompletion(j, from, core.ModeHooks, timeout, poll, stopOnPrompt)
	}
	if poll <= 0 {
		poll = defaultPoll
	}
	deadline := time.Now().Add(timeout)

	sc := NewScanner(from)
	off := from
	seenC := false
	var heldD *Mark
	var graceUntil time.Time

	done := func(m Mark) RunResult {
		return RunResult{Outcome: OutcomeDone, ExitCode: m.ExitCode, MarkStart: m.Start, EndOffset: m.End}
	}

	for {
		if size := j.Size(); size > off {
			data, err := j.ReadRange(off, size)
			if err != nil {
				return RunResult{}, fmt.Errorf("detect: wait correlated: %w", err)
			}
			if len(data) > 0 {
				off += int64(len(data))
				for _, m := range sc.Feed(data) {
					if m.Start < from {
						continue
					}
					switch m.Kind {
					case MarkC:
						if !seenC {
							seenC = true
							heldD = nil // any held D preceded C in the stream: it was a stray
						}
					case MarkD:
						if seenC {
							return done(m), nil
						}
						if heldD == nil {
							d := m
							heldD = &d
							graceUntil = time.Now().Add(graceNoC)
						}
					}
				}
			}
		}

		now := time.Now()
		if heldD != nil && !now.Before(graceUntil) {
			return done(*heldD), nil
		}
		if stopOnPrompt {
			if status, prompt := Refine(j, core.StatusRunning, core.ModeHooks); status == core.StatusAwaitingInput {
				return RunResult{
					Outcome: OutcomeAwaitingInput, ExitCode: -1, MarkStart: -1,
					EndOffset: off, Prompt: prompt,
				}, nil
			}
		}
		if !now.Before(deadline) {
			if heldD != nil {
				return done(*heldD), nil
			}
			return RunResult{Outcome: OutcomeTimeout, ExitCode: -1, MarkStart: -1, EndOffset: off}, nil
		}

		// Sleep no longer than the nearest of poll, grace expiry, and deadline
		// so grace acceptance is not delayed by a coarse poll interval.
		sleep := poll
		if heldD != nil {
			if rem := time.Until(graceUntil); rem < sleep {
				sleep = rem
			}
		}
		if rem := time.Until(deadline); rem < sleep {
			sleep = rem
		}
		if sleep < time.Millisecond {
			sleep = time.Millisecond
		}
		time.Sleep(sleep)
	}
}
