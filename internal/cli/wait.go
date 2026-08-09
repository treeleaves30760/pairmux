package cli

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/detect"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/notify"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/shape"
	"github.com/treeleaves30760/pairmux/internal/shellhooks"
	"github.com/treeleaves30760/pairmux/internal/state"
)

const (
	waitPoll = 200 * time.Millisecond

	// answeredSettle is how long a handoff terminal must keep looking like it has
	// moved past its prompt before --human calls the human done. A rejected answer
	// is echoed before the program re-prompts, and without this hold that gap
	// would read as progress.
	answeredSettle = 500 * time.Millisecond

	// handoffRetryFloor is the shortest deadline a timed-out handoff will suggest
	// waiting again for. A handoff is paced by a person, and the skill's rule is
	// that its wait is never shortened below pairmux's 300s default — the hint an
	// agent is told to follow must not be the thing that breaks it.
	handoffRetryFloor = 300 * time.Second
)

// cmdWait blocks until a terminal satisfies a condition: the running command
// finishing (--done), new output matching --pattern, a new human note event
// (--human), or quiescence (--idle MS, the default). It is read-only — it
// records no events and takes no lock — so a human and any number of agents can
// wait on the same terminal at once, which is what makes --done a broadcast:
// every subscriber wakes on the one completion mark.
func (c *Ctx) cmdWait(args []string) int {
	const usageLine = `pairmux wait <name> [--idle MS] [--pattern RE] [--human] [--done] [--notify] [--timeout 300s]`

	var idleS, patternS, timeoutS string
	var human, notifyHuman, doneFlag bool
	seen := map[string]bool{}
	pos, err := parseFlags(args, flagSpec{
		bools: map[string]*bool{"human": &human, "notify": &notifyHuman, "done": &doneFlag},
		vals:  map[string]*string{"idle": &idleS, "pattern": &patternS, "timeout": &timeoutS},
		seen:  seen,
	})
	if err != nil {
		return c.usage(usageLine, err.Error())
	}
	if len(pos) == 0 {
		return c.usage(usageLine, "pairmux ls")
	}
	if len(pos) > 1 {
		return c.usage(usageLine, "unexpected argument "+pos[1])
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	name := pos[0]
	if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
		return rc
	}

	idleMS := 800
	if idleS != "" {
		v, err := strconv.Atoi(idleS)
		if err != nil || v <= 0 {
			return c.usage(usageLine, "bad --idle (milliseconds): "+idleS)
		}
		idleMS = v
	}
	timeout := 300 * time.Second
	if timeoutS != "" {
		d, err := time.ParseDuration(timeoutS)
		if err != nil {
			return c.usage(usageLine, "bad --timeout: "+err.Error())
		}
		timeout = d
	}
	var patternRE *regexp.Regexp
	if seen["pattern"] {
		re, err := regexp.Compile(patternS)
		if err != nil {
			return c.fail(output.CodeBadArgs, "bad --pattern: "+err.Error(),
				`RE2 syntax, e.g. pairmux wait `+name+` --pattern "error|panic"`)
		}
		patternRE = re
	}
	// Idle is the default condition; an explicit --idle arms it alongside
	// --pattern/--human/--done. First condition satisfied wins. --done must not
	// inherit it: a subscriber asking for the next completion would otherwise
	// return instantly on the idle terminal it is subscribing to.
	waitIdle := (!seen["pattern"] && !seen["human"] && !seen["done"]) || seen["idle"]

	term, err := state.ResolveAt(c.Tmux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.tmuxErr(err)
	}
	if !term.Alive {
		return c.fail(output.CodeDead, fmt.Sprintf("terminal %q is dead", name), "pairmux new")
	}
	program := term.Meta.Shell == ""
	if doneFlag && program {
		return c.fail(output.CodeBadArgs, "this terminal runs a program, not a shell",
			fmt.Sprintf("--done needs shell completion marks; use pairmux wait %s --pattern RE or --idle 800", name))
	}
	j, err := journal.Open(term.Dir)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}

	// Baselines: only bytes and events that arrive after wait starts count as
	// new for the polled conditions.
	baseSize := j.Size()
	baseEvents := 0
	var startEvs []core.Event
	if evs, err := j.Events(); err == nil {
		startEvs = evs
		baseEvents = len(evs)
	}

	// Completion baseline. Nothing writes cmd_end while wait blocks — settlement
	// is lazy and wait is read-only — so "the command finished" can only be read
	// from the completion marks in raw.log, never from the event log. That is also
	// why a cmd_start with no cmd_end is no proof a command is still running: one
	// that finished during an earlier wait stays pending until the next run
	// records its end. Derive the real status before trusting the event.
	startStatus, _ := detect.Refine(j, deriveTerminalStatus(j, term.Alive, term.Mode, program), term.Mode)
	pending, hasPending, _ := j.PendingCmd()
	inFlight := hasPending && (startStatus == core.StatusRunning || startStatus == core.StatusAwaitingInput)

	// The two conditions want opposite things from an unsettled completion.
	// --done subscribes to a future one, so on an idle terminal it watches the
	// next command rather than being handed a result older than the subscription.
	// --human watches the pending command either way: a handoff exists because the
	// agent is blocked on that command, and a human who answers the prompt in the
	// pane leaves no note and no event behind, so the completion mark is the only
	// evidence there is. An already-present one is evidence the agent has not seen
	// yet — the pre-existing-note rule below says such a signal resolves the wait
	// rather than being ignored, and an agent that hands off, does other work, and
	// only then waits would otherwise be back to blocking for nothing.
	watchPending := inFlight || (human && hasPending)

	var completion *detect.CompletionWatcher
	var completionFrom int64
	var completionEcho, completionFull string
	var completionStart time.Time
	if doneFlag || (human && hasPending) {
		completionFrom, completionFull = baseSize, fmt.Sprintf("pairmux log %s --range 1:end", name)
		if watchPending {
			completionFrom, completionEcho, completionStart = pending.Offset, pending.Text, pending.TS
			completionFull = fmt.Sprintf("pairmux log %s --cmd %d", name, pending.CmdID)
			if term.Mode == core.ModeSentinel {
				completionEcho += shellhooks.SentinelSuffix(term.Meta.Shell)
			}
		}
		completion = detect.NewCompletionWatcher(completionFrom, term.Mode)
	}

	// What a handoff is actually waiting for is the human finishing, not the
	// command finishing: answering the password of a five-minute migration ends
	// the handoff at second two. So a --human wait armed at a prompt resolves as
	// soon as the terminal is visibly moving again — output has appeared past the
	// prompt and the last line is no longer prompt-shaped. Completion stays armed
	// underneath for the cases this cannot see (a program that prints nothing
	// after the answer, or a command that had already finished).
	watchAnswered := human && startStatus == core.StatusAwaitingInput

	var extraNext []string
	finish := func(e output.Envelope) int {
		e.Terminal = name
		e.Mode = string(term.Mode)
		if len(extraNext) > 0 {
			e.Next = append(append([]string{}, extraNext...), e.Next...)
		}
		// The R3 large-journal guard must be reachable from wait: a program
		// terminal (dev server, tail -f) is driven by send/wait/peek and never
		// sees run's guard, yet is exactly the workload that grows fastest.
		e.Next = appendGuard(e.Next, j, name)
		return c.emit(e)
	}
	peekHint := fmt.Sprintf("pairmux peek %s", name)
	// A wait that times out has not failed — the agent is meant to wait again, and
	// the hint it follows must be the *same* wait with a longer deadline. Rebuild
	// it from the parsed flags: a hint that dropped --human turned the retry into
	// a plain idle wait, which at the handoff prompt returns instantly and puts
	// the agent straight back where it started.
	retryHint := waitRetryHint(name, retryFlags(seen, idleS, patternS, human, doneFlag, notifyHuman),
		retryTimeout(timeout, human))

	// A note the human already left — and no command has consumed — satisfies
	// --human immediately: the natural ordering is "human notes, THEN the agent
	// waits", so an unseen pre-existing note must not be ignored. Checked before
	// --notify fires so a human whose note is already waiting is not re-pinged.
	if human {
		if note, ok := latestUnseenNote(startEvs); ok {
			return finish(output.Envelope{Status: "human-done", Output: note, Next: []string{peekHint}})
		}
	}

	if notifyHuman {
		if err := notify.Notify("pairmux", fmt.Sprintf("terminal %s needs your attention", name)); err != nil {
			extraNext = append(extraNext, "notification failed — check the terminal manually")
		}
	}
	deadline := time.Now().Add(timeout)
	var lastStateCheck, answeredAt time.Time
	answerScan, answerSubmitted := baseSize, false
	for {
		if patternRE != nil {
			if data, err := j.ReadRange(baseSize, -1); err == nil && len(data) > 0 {
				if hit, ok := matchShapedPattern(data, patternRE); ok {
					return finish(output.Envelope{Status: "pattern-found", Output: hit, Next: []string{peekHint}})
				}
			}
		}
		if human {
			if evs, err := j.Events(); err == nil {
				if note, ok := newNote(evs, baseEvents); ok {
					return finish(output.Envelope{Status: "human-done", Output: note, Next: []string{peekHint}})
				}
			}
		}
		if completion != nil {
			res, done, err := completion.Poll(j)
			if err != nil {
				return c.fail(output.CodeInternal, err.Error(), "")
			}
			if done {
				return finish(completionEnvelope(j, res, completionFrom, completionEcho, completionFull, completionStart, peekHint, !human))
			}
		}
		// Keystrokes echo into the journal one at a time, so "output appeared" on
		// its own reads a human halfway through typing as a human who has finished.
		// What separates the two is the newline their Enter produces — either echoed
		// by the tty or written by the program as it accepts the line — so the line
		// the prompt sat on must be terminated before any verdict is possible.
		if watchAnswered && !answerSubmitted {
			if size := j.Size(); size > answerScan {
				if data, err := j.ReadRange(answerScan, size); err == nil && len(data) > 0 {
					answerScan += int64(len(data))
					answerSubmitted = bytes.IndexByte(data, '\n') >= 0
				}
			}
		}
		if watchAnswered && answerSubmitted {
			if _, stillPrompting := detect.PromptPending(j); stillPrompting {
				// Either the prompt is still the last thing on screen, or the program
				// rejected the answer and asked again. Both mean the human is not done.
				answeredAt = time.Time{}
			} else {
				if answeredAt.IsZero() {
					answeredAt = time.Now()
				}
				// Hold the verdict briefly: a rejected answer echoes first and the
				// re-prompt lands a moment later, and reporting "moving again" in that
				// gap would send the agent back round the handoff for nothing.
				if time.Since(answeredAt) >= answeredSettle {
					return c.finishAnswered(finish, name, j, term.Mode, program)
				}
			}
		}
		idleFor := time.Duration(idleMS) * time.Millisecond
		stateCheckDue := waitIdle || time.Since(lastStateCheck) >= time.Second
		if stateCheckDue && journalQuiet(j, idleFor) {
			lastStateCheck = time.Now()
			// Liveness can change while wait blocks. Refresh it only once output
			// is quiet, then distinguish true idle from a quiet running command,
			// an input prompt, or a pane that died during the wait.
			current, err := state.ResolveAt(c.Tmux, c.StateDir, name)
			if err != nil {
				if errors.Is(err, state.ErrNotFound) {
					return finish(output.Envelope{Status: string(core.StatusDead), Output: waitCurrentTail(j), Next: []string{
						fmt.Sprintf("pairmux log %s", name), "pairmux new",
					}})
				}
				return c.tmuxErr(err)
			}
			status, prompt, terminal := terminalStatusAfterQuiet(j, current.Alive, term.Mode, current.Meta.Shell == "", idleFor)
			// awaiting-input is the reason a handoff exists, never its outcome:
			// returning it to an agent that is already handing off only sends it
			// round the same loop. --human waits for the prompt to be answered.
			if handoffPrompt := human && status == core.StatusAwaitingInput; terminal &&
				(waitIdle || status == core.StatusDead) && !handoffPrompt {
				next := peekNext(name, status)
				if status == core.StatusAwaitingInput {
					next = awaitingNext(name, prompt)
				}
				body := ""
				switch status {
				case core.StatusAwaitingInput:
					body = prompt
				case core.StatusDead:
					body = waitCurrentTail(j)
				}
				return finish(output.Envelope{Status: string(status), Output: body, Next: next})
			}
		}
		if !time.Now().Before(deadline) {
			next := []string{peekHint, retryHint}
			if human {
				// A handoff that times out means the human has not come yet — the one
				// thing the agent must not conclude is that it should act instead.
				next = []string{"the human has not answered yet — do NOT type the secret", retryHint, peekHint}
			}
			return finish(output.Envelope{Status: "timeout", Next: next})
		}
		time.Sleep(waitPoll)
	}
}

// retryFlags rebuilds the condition flags of the wait being retried, in the
// order the usage line lists them. Only conditions the caller actually asked for
// are reproduced — an unrequested --idle would arm a condition the original wait
// did not have. Pure: unit-tested directly.
func retryFlags(seen map[string]bool, idleS, patternS string, human, done, notify bool) []string {
	var flags []string
	if seen["idle"] {
		flags = append(flags, "--idle", idleS)
	}
	if seen["pattern"] {
		flags = append(flags, "--pattern", quoteToken(patternS))
	}
	if done {
		flags = append(flags, "--done")
	}
	if human {
		flags = append(flags, "--human")
	}
	if notify {
		flags = append(flags, "--notify") // the human did not come; ping them again
	}
	return flags
}

// retryTimeout doubles the deadline that just expired, never dropping a handoff
// below the floor the skill requires of it. Pure: unit-tested directly.
func retryTimeout(timeout time.Duration, human bool) time.Duration {
	next := 2 * timeout
	if human && next < handoffRetryFloor {
		return handoffRetryFloor
	}
	return next
}

// waitRetryHint renders the retry as a runnable command. Pure: unit-tested directly.
func waitRetryHint(name string, flags []string, timeout time.Duration) string {
	parts := append([]string{"pairmux", "wait", name}, flags...)
	parts = append(parts, "--timeout", timeout.String())
	return strings.Join(parts, " ")
}

// finishAnswered reports a handoff whose prompt has been answered: the terminal
// is moving again, which is the news the agent was blocked for. It re-resolves
// liveness because output stopping and the pane dying look identical from the
// journal, and it teaches --done — "running" is an invitation to keep following
// the command, not a dead end. Like every resolved handoff it carries no output
// (see completionEnvelope), except for a dead pane, where the agent needs the
// tail to work out what happened at all.
func (c *Ctx) finishAnswered(finish func(output.Envelope) int, name string, j *journal.Journal, mode core.Mode, program bool) int {
	alive := true
	if current, err := state.ResolveAt(c.Tmux, c.StateDir, name); err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			return c.tmuxErr(err)
		}
		alive = false
	} else {
		alive = current.Alive
	}
	status := deriveTerminalStatus(j, alive, mode, program)
	next := peekNext(name, status)
	body := ""
	switch status {
	case core.StatusRunning:
		next = append([]string{fmt.Sprintf("pairmux wait %s --done", name)}, next...)
	case core.StatusDead:
		body = waitCurrentTail(j)
	}
	return finish(output.Envelope{Status: string(status), Output: body, Next: next})
}

// completionEnvelope renders a command that finished while wait was blocked. It
// is run's "done" shape minus the cmd_end record: wait stays read-only, so the
// event is left for the next run to settle lazily. started is the cmd_start
// timestamp when one is known — a --done subscriber that armed on an idle
// terminal has none, and reports no duration rather than a fabricated one.
//
// reveal is false for a --human handoff, which withholds the output entirely.
// The span this envelope would quote is exactly the span a human was summoned
// to type into: a program that echoes what it should not (or a secret prompt the
// heuristics never recognized) would put the credential straight into the
// agent's context — the one thing the handoff promises will not happen. The
// agent still gets the fact and the exit code, and peeks if it wants the output.
func completionEnvelope(j *journal.Journal, res detect.RunResult, from int64, echo, getFull string, started time.Time, peekHint string, reveal bool) output.Envelope {
	ec := res.ExitCode
	env := output.Envelope{Status: "done", ExitCode: &ec, Next: []string{peekHint}}
	if !started.IsZero() {
		ms := time.Since(started).Milliseconds()
		env.DurationMS = &ms
	}
	if !reveal {
		return env
	}
	raw, _ := j.ReadRange(from, res.MarkStart)
	body, omitted := lastLines(cleanNoTrunc(raw, echo), 20)
	env.Output = body
	if omitted > 0 {
		env.Truncated = &output.TruncInfo{OmittedLines: omitted, GetFull: getFull}
	}
	return env
}

// matchShapedPattern shapes raw journal bytes (CR resolution + ANSI strip) and
// returns the first line matching re, prefixed with up to two preceding lines
// of context. Pure: unit-tested directly.
func matchShapedPattern(raw []byte, re *regexp.Regexp) (string, bool) {
	shaped := string(shape.StripANSI(shape.CollapseCR(raw)))
	lines := strings.Split(shaped, "\n")
	for i, line := range lines {
		if re.MatchString(line) {
			start := i - 2
			if start < 0 {
				start = 0
			}
			return strings.Join(lines[start:i+1], "\n"), true
		}
	}
	return "", false
}

// latestUnseenNote returns the newest EvNote not yet consumed by a command:
// notes with TS after the last EvCmdEnd, or every note when no command has
// ended. This replicates commands.go's unseenNotes definition (kept local per
// the wave split — wait.go must not couple to commands.go), reduced to the
// latest text. Pure: unit-tested directly.
func latestUnseenNote(evs []core.Event) (string, bool) {
	var lastEnd time.Time
	for _, ev := range evs {
		if ev.Type == core.EvCmdEnd {
			lastEnd = ev.TS
		}
	}
	text, found := "", false
	for _, ev := range evs {
		if ev.Type == core.EvNote && (lastEnd.IsZero() || ev.TS.After(lastEnd)) {
			text, found = ev.Text, true // keep scanning: the latest note wins
		}
	}
	return text, found
}

// newNote returns the text of the first EvNote recorded after the baseline
// event count. Pure: unit-tested directly.
func newNote(evs []core.Event, base int) (string, bool) {
	if base < 0 {
		base = 0
	}
	if base > len(evs) {
		base = len(evs)
	}
	for _, ev := range evs[base:] {
		if ev.Type == core.EvNote {
			return ev.Text, true
		}
	}
	return "", false
}

// journalQuiet reports whether raw.log has been untouched for at least idle.
// A missing raw.log counts as quiet — there is no activity at all.
func journalQuiet(j *journal.Journal, idle time.Duration) bool {
	mt, ok := j.LastModified()
	if !ok {
		return true
	}
	return time.Since(mt) >= idle
}

// terminalStatusAfterQuiet prevents output quiescence from masquerading as
// terminal idleness. It completes the wait only for actionable terminal
// states: true idle, awaiting input, or dead. A quiet running/unknown command
// keeps waiting and can time out without suggesting another run.
func terminalStatusAfterQuiet(j *journal.Journal, alive bool, mode core.Mode, program bool, idle time.Duration) (core.Status, string, bool) {
	if !journalQuiet(j, idle) {
		return core.StatusUnknown, "", false
	}
	status, prompt := detect.Refine(j, deriveTerminalStatus(j, alive, mode, program), mode)
	switch status {
	case core.StatusIdle, core.StatusAwaitingInput, core.StatusDead:
		return status, prompt, true
	default:
		return status, prompt, false
	}
}

func waitCurrentTail(j *journal.Journal) string {
	raw, _, err := j.TailBytes(64 * 1024)
	if err != nil {
		return ""
	}
	body, _ := lastLines(cleanNoTrunc(raw, ""), 20)
	return body
}
