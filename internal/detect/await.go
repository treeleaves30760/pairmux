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
)

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
	line, prompt := PromptPending(j)
	if !prompt {
		return st, ""
	}
	return core.StatusAwaitingInput, line
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
			if status, prompt := Refine(j, core.StatusRunning, core.ModeHooks); status == core.StatusAwaitingInput {
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
