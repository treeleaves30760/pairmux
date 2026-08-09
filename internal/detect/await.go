package detect

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
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

	// inferQuiet gates the weakest evidence: a line-oriented terminal sitting on
	// an unterminated line that no pattern recognises. That shape also describes
	// a command which printed "Building... " and went away to work, so silence
	// has to run far longer than for a recognised prompt before anyone calls it
	// a question. Ten seconds is chosen against the two costs: a real question
	// waits forever, so ten seconds of it is nothing next to running out a 60s
	// command timeout, while a build that stays silent mid-line that long is
	// unusual — most emit progress, and progress ends its lines. When the guess
	// is wrong nothing is broken: `run` never kills the command, and the reply
	// says to look before answering and offers --done for carrying on.
	inferQuiet = 10 * time.Second
)

// PromptKind says how much is known about what a quiet terminal wants, because
// the three cases call for three different responses from an agent.
type PromptKind int

const (
	// KindNone: nothing indicates the terminal is waiting for input.
	KindNone PromptKind = iota
	// KindSecret: an answer must never be typed, guessed, echoed or logged by
	// an agent. Established from the terminal's own discipline where possible
	// and from wording otherwise.
	KindSecret
	// KindOpen: an ordinary question an agent may answer with `send`.
	KindOpen
	// KindInferred: the terminal looks like it is waiting, but only because it
	// has been silent on an unterminated line — no pattern matched and the
	// terminal's discipline did not settle it. Confirm before answering.
	KindInferred
)

// Prompt is what a quiet terminal appears to be waiting for.
type Prompt struct {
	Kind PromptKind
	Line string // the shaped prompt line; empty when only the terminal spoke
}

// Secret reports whether an agent must refuse to answer.
func (p Prompt) Secret() bool { return p.Kind == KindSecret }

// Waiting reports whether the terminal appears to want input at all.
func (p Prompt) Waiting() bool { return p.Kind != KindNone }

// Interactive-prompt heuristics, compiled once. Lines are matched after
// CollapseCR+StripANSI shaping and whitespace trimming. The pager rule covers
// less, which renders its status line as "<filename> (END)" (suffix, not
// prefix) and shows a bare ":" while mid-file, and more, whose "--More--(n%)"
// can carry surrounding text — hence: --More-- anywhere, "(END)" as a line
// suffix, or a line that is exactly ":".
//
// The secret rule is two alternations because RE2 word boundaries are
// ASCII-only: Latin-script keywords sit inside \b...\b, while CJK/Hangul/
// Cyrillic keywords (sudo's 密碼/密码/パスワード/암호/пароль translations)
// must match bare. The prompt must still end in an ASCII or fullwidth colon.
// Recognition is best-effort and English-biased — see PAIRMUX_SECRET_PROMPT_RE
// for extending it without a release.
var (
	reSecretPrompt = regexp.MustCompile(`(?i)(?:\b(?:` +
		`pass\s?(?:word|phrase|code)|passwort|contraseña|senha|mot de passe|` +
		`pin|otp|mfa|2fa|secret|token|credentials?|` +
		`(?:verification|security|auth(?:entication)?|access) code|` +
		`(?:api|encryption|private|ssh|gpg) key` +
		`)\b|密碼|密码|パスワード|暗証番号|비밀번호|암호|пароль).*[:：]\s*$`)
	reConfirmYN   = regexp.MustCompile(`\[[yY]/[nN](?:/[a-zA-Z]+)*\]\s*\??\s*$`)
	reConfirmWord = regexp.MustCompile(`(?i)\((?:yes/no|y/n)(?:/\w+)*\)\s*\??\s*$`)
	rePager       = regexp.MustCompile(`--More--|\(END\)\s*$|^:\s*$`)
	rePressKey    = regexp.MustCompile(`(?i)press (any key|enter|return)`)
)

// SecretPromptEnv names the user extension point for secret-prompt
// recognition: an RE2 pattern OR'd with (never replacing) the builtin
// heuristics, for locales and tools the builtin list misses. An invalid
// pattern is ignored here (fail-safe) and surfaced by doctor.
const SecretPromptEnv = "PAIRMUX_SECRET_PROMPT_RE"

var (
	extraSecretMu  sync.Mutex
	extraSecretSrc string
	extraSecretVal *regexp.Regexp
)

// extraSecretRE compiles the SecretPromptEnv extension, caching per env value
// so repeated Refine polling does not recompile.
func extraSecretRE() *regexp.Regexp {
	src := os.Getenv(SecretPromptEnv)
	extraSecretMu.Lock()
	defer extraSecretMu.Unlock()
	if src == extraSecretSrc {
		return extraSecretVal
	}
	extraSecretSrc = src
	extraSecretVal = nil
	if src != "" {
		if re, err := regexp.Compile(src); err == nil {
			extraSecretVal = re
		}
	}
	return extraSecretVal
}

// isInteractivePrompt reports whether a shaped, trimmed line looks like a
// program waiting for a human answer. A line matching the user's secret
// extension counts: it must be able to upgrade running to awaiting-input, or
// LooksSecret would never be consulted for it.
func isInteractivePrompt(line string) bool {
	if strings.TrimSpace(line) == ">>>" ||
		reSecretPrompt.MatchString(line) ||
		reConfirmYN.MatchString(line) ||
		reConfirmWord.MatchString(line) ||
		rePager.MatchString(line) ||
		rePressKey.MatchString(line) {
		return true
	}
	re := extraSecretRE()
	return re != nil && re.MatchString(line)
}

// LooksSecret reports whether prompt belongs to the password/passphrase/
// passcode class, i.e. an answer to it must never be echoed or logged.
// Recognition is heuristic: a miss degrades to a quiet running terminal, so
// callers that know a command needs credentials should treat a prolonged
// quiet "running" as a handoff candidate regardless.
func LooksSecret(prompt string) bool {
	p := strings.TrimSpace(prompt)
	if reSecretPrompt.MatchString(p) {
		return true
	}
	re := extraSecretRE()
	return re != nil && re.MatchString(p)
}

// Refine is Classify for callers with no pane terminal to hand — `watch`, and
// terminals created before pairmux recorded one. Wording is then the only
// evidence available, which is where this detection started.
func Refine(j *journal.Journal, st core.Status, mode core.Mode) (core.Status, string) {
	st, p := Classify(j, st, mode, "")
	return st, p.Line
}

// Classify upgrades StatusRunning to StatusAwaitingInput when a quiet terminal
// appears to be waiting for input, and says what kind of waiting it is.
//
// Evidence is taken in order of how much it can be trusted:
//
//  1. The pane's line discipline. ECHO off with ICANON on is the getpass
//     signature and settles the secret case outright — no wording involved, so
//     sudo in any locale, an ssh passphrase, pinentry and a tool nobody has
//     seen yet all classify correctly. This is the case where being wrong is
//     worst, and the only one that can be made certain.
//  2. The wording of the last line. Recognised prompts keep working exactly as
//     before, and still promote to KindSecret for tools that ask for a
//     credential in plain sight without touching echo.
//  3. Silence on an unterminated line. Weakest, because a command that printed
//     a partial line and went to work looks identical, so it needs inferQuiet
//     rather than refineQuiet of silence and is reported as KindInferred for
//     the caller to hedge on. It is what lets an unrecognised question — a
//     terraform "Enter a value:", an ssh host-key prompt — end a handoff at all
//     instead of running out the clock.
//
// Cheap and non-blocking: an mtime check, a bounded tail read, and at most one
// ioctl. Read-only, so any number of agents may call it on one terminal at
// once. mode is accepted for signature stability; the rules do not vary by it.
func Classify(j *journal.Journal, st core.Status, mode core.Mode, tty string) (core.Status, Prompt) {
	if st != core.StatusRunning {
		return st, Prompt{}
	}
	mt, ok := j.LastModified()
	if !ok {
		return st, Prompt{}
	}
	quiet := time.Since(mt)
	if quiet < refineQuiet {
		return st, Prompt{}
	}

	line, recognized := PromptPending(j)
	p := classify(ReadTTYState(tty), line, recognized, quiet, unterminatedTail(j))
	if !p.Waiting() {
		return st, Prompt{}
	}
	return core.StatusAwaitingInput, p
}

// classify is the decision itself, separated from gathering its inputs so every
// branch is reachable from a test without a live terminal.
//
// A raw-mode program — a pager, a TUI, or a shell's own line editor — makes the
// discipline uninformative: it reads keystrokes itself, so echo says nothing
// about whether it wants one. Wording is all there is in that case, which is
// why the recognised-pattern branch sits between the two terminal-state ones
// rather than after them.
func classify(disc TTYState, line string, recognized bool, quiet time.Duration, unterminated bool) Prompt {
	switch {
	case disc.Secret():
		return Prompt{Kind: KindSecret, Line: line}
	case recognized:
		kind := KindOpen
		if LooksSecret(line) {
			kind = KindSecret
		}
		return Prompt{Kind: kind, Line: line}
	case disc.LineOriented() && quiet >= inferQuiet && unterminated:
		return Prompt{Kind: KindInferred, Line: line}
	}
	return Prompt{}
}

// unterminatedTail reports whether the journal's last byte leaves the cursor
// part-way through a line, which is what a prompt does and what a command that
// has finished speaking does not. Derived from the journal rather than the
// pane's cursor so that it costs no tmux call: several agents polling one
// terminal would each pay for that.
func unterminatedTail(j *journal.Journal) bool {
	data, _, err := j.TailBytes(refineTail)
	if err != nil || len(data) == 0 {
		return false
	}
	shaped := shape.StripANSI(shape.CollapseCR(data))
	if len(shaped) == 0 {
		return false
	}
	switch shaped[len(shaped)-1] {
	case '\n', '\r':
		return false
	}
	return true
}

// PromptPending reports the journal's current last non-blank line and whether it
// still looks like a program waiting for an answer. It is Refine without the
// quiescence requirement, for a caller watching a prompt it already knows about:
// such a caller does not need to be told a prompt exists, it needs to know
// whether that prompt is still the last thing on screen *right now*, including
// while output is flowing. Read-only and non-blocking.
func PromptPending(j *journal.Journal) (string, bool) {
	data, _, err := j.TailBytes(refineTail)
	if err != nil || len(data) == 0 {
		return "", false
	}
	line := lastNonBlankLine(string(shape.StripANSI(shape.CollapseCR(data))))
	if line == "" {
		return "", false
	}
	return line, isInteractivePrompt(line)
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
	return waitCompletionCorrelated(j, from, timeout, poll, requireC, false, "")
}

// WaitCommandCorrelated is the prompt-aware form used by hooks-mode `run`.
// Completion correlation retains priority over the prompt outcome. tty carries
// the pane's terminal device for classification; "" falls back to wording.
func WaitCommandCorrelated(j *journal.Journal, from int64, timeout, poll time.Duration, requireC bool, tty string) (RunResult, error) {
	return waitCompletionCorrelated(j, from, timeout, poll, requireC, true, tty)
}

func waitCompletionCorrelated(j *journal.Journal, from int64, timeout, poll time.Duration, requireC, stopOnPrompt bool, tty string) (RunResult, error) {
	if !requireC {
		return waitCompletion(j, from, core.ModeHooks, timeout, poll, stopOnPrompt, tty)
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
							return doneResult(m), nil
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
			return doneResult(*heldD), nil
		}
		if stopOnPrompt {
			if status, prompt := Classify(j, core.StatusRunning, core.ModeHooks, tty); status == core.StatusAwaitingInput {
				return RunResult{
					Outcome: OutcomeAwaitingInput, ExitCode: -1, MarkStart: -1,
					EndOffset: off, Prompt: prompt,
				}, nil
			}
		}
		if !now.Before(deadline) {
			if heldD != nil {
				return doneResult(*heldD), nil
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
