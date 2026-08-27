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

// waitTarget is one terminal's share of a wait: the baselines it was armed
// with, and the per-condition state that has to survive between polls. One is
// built per name so a wait over several terminals is genuinely one wait rather
// than a race between several — the conditions are evaluated against each
// terminal independently and the first to fire ends it for all of them.
type waitTarget struct {
	name string
	term *state.Terminal
	j    *journal.Journal

	baseSize   int64
	baseEvents int

	// completion: --done, and --human's fallback for a prompt whose program
	// says nothing after it is answered.
	completion      *detect.CompletionWatcher
	completionFrom  int64
	completionEcho  string
	completionFull  string
	completionStart time.Time

	// answered: --human's primary signal, that the prompt is no longer on screen.
	// All four are set together by armAnswered, which may run at arm time or on
	// any later poll — a handoff can be armed before its prompt exists.
	watchAnswered   bool
	answerScan      int64
	answerSubmitted bool
	answeredAt      time.Time

	lastStateCheck time.Time
	peekHint       string
}

// cmdWait blocks until one of its terminals satisfies a condition: the running
// command finishing (--done), new output matching --pattern, a new note event
// (--note, or --human with its handoff extras), or quiescence (--idle MS, the
// default). It is read-only — it records no events and takes no lock — so a
// human and any number of agents can wait on the same terminal at once, which
// is what makes --done a broadcast: every subscriber wakes on the one
// completion mark.
//
// Several names may be given, separated by spaces or commas. The wait then ends
// on the first terminal to satisfy a condition and the envelope names it. This
// is the join an agent driving a fleet of terminals needs: without it, waiting
// on five sub-agents means five processes polling five deadlines, and whichever
// one finishes first cannot wake the others.
//
// --note is the note condition on its own. --human is the handoff composite:
// notes, plus the prompt being answered, plus the command completing — three
// signals because a human at a keyboard may leave any one of them behind. An
// agent waiting for another agent's turn to end wants only the first, and wants
// it without --human's habit of returning early on a terminal that was already
// sitting at a prompt when the wait was armed.
func (c *Ctx) cmdWait(args []string) int {
	const usageLine = `pairmux wait <name>[,<name>...] [--idle MS] [--pattern RE] [--note] [--human] [--done] [--notify] [--timeout 300s]`

	var idleS, patternS, timeoutS string
	var human, notifyHuman, doneFlag, noteFlag bool
	seen := map[string]bool{}
	pos, err := parseFlags(args, flagSpec{
		bools: map[string]*bool{"human": &human, "notify": &notifyHuman, "done": &doneFlag, "note": &noteFlag},
		vals:  map[string]*string{"idle": &idleS, "pattern": &patternS, "timeout": &timeoutS},
		seen:  seen,
	})
	if err != nil {
		return c.usage(usageLine, err.Error())
	}
	names, err := waitNames(pos)
	if err != nil {
		return c.usage(usageLine, err.Error())
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	for _, name := range names {
		if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
			return rc
		}
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
				`RE2 syntax, e.g. pairmux wait `+names[0]+` --pattern "error|panic"`)
		}
		patternRE = re
	}
	// Idle is the default condition; an explicit --idle arms it alongside the
	// others. First condition satisfied wins. --done must not inherit it: a
	// subscriber asking for the next completion would otherwise return instantly
	// on the idle terminal it is subscribing to. Nor must --note, whose whole
	// point is to block until something signals.
	waitIdle := (!seen["pattern"] && !seen["human"] && !seen["done"] && !seen["note"]) || seen["idle"]
	// The note condition itself. --human arms it too, alongside its extras.
	waitNote := human || noteFlag

	targets := make([]*waitTarget, 0, len(names))
	for _, name := range names {
		t, rc, failed := c.armWaitTarget(name, human, doneFlag, noteFlag)
		if failed {
			return rc
		}
		targets = append(targets, t)
	}

	var extraNext []string
	finish := func(t *waitTarget, e output.Envelope) int {
		e.Terminal = t.name
		e.Mode = string(t.term.Mode)
		if len(extraNext) > 0 {
			e.Next = append(append([]string{}, extraNext...), e.Next...)
		}
		// The R3 large-journal guard must be reachable from wait: a program
		// terminal (dev server, tail -f) is driven by send/wait/peek and never
		// sees run's guard, yet is exactly the workload that grows fastest.
		e.Next = appendGuard(e.Next, t.j, t.name)
		return c.emit(e)
	}
	// A wait that times out has not failed — the agent is meant to wait again, and
	// the hint it follows must be the *same* wait with a longer deadline. Rebuild
	// it from the parsed flags: a hint that dropped --human turned the retry into
	// a plain idle wait, which at the handoff prompt returns instantly and puts
	// the agent straight back where it started.
	retryHint := waitRetryHint(names, retryFlags(seen, idleS, patternS, human, doneFlag, noteFlag, notifyHuman),
		retryTimeout(timeout, human))

	// A note the human already left — and no command or send has consumed —
	// satisfies the note condition immediately: the natural ordering is "the
	// other side signals, THEN the agent waits", so an unseen pre-existing note
	// must not be ignored. Checked before --notify fires so a human whose note is
	// already waiting is not re-pinged.
	if waitNote {
		for _, t := range targets {
			if evs, err := t.j.Events(); err == nil {
				if note, ok := latestUnseenNote(evs); ok {
					return finish(t, noteEnvelope(t, note, human))
				}
			}
		}
	}

	if notifyHuman {
		if err := notify.Notify("pairmux", fmt.Sprintf("terminal %s needs your attention", strings.Join(names, ", "))); err != nil {
			extraNext = append(extraNext, "notification failed — check the terminal manually")
		}
	}

	idleFor := time.Duration(idleMS) * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		for _, t := range targets {
			env, done, rc, failed := c.pollWaitTarget(t, waitPollSpec{
				patternRE: patternRE,
				waitNote:  waitNote,
				human:     human,
				waitIdle:  waitIdle,
				idleFor:   idleFor,
			})
			if failed {
				return rc
			}
			if done {
				return finish(t, env)
			}
		}
		if !time.Now().Before(deadline) {
			next := []string{targets[0].peekHint, retryHint}
			if human {
				// A handoff that times out means the human has not come yet — the one
				// thing the agent must not conclude is that it should act instead.
				next = []string{"the human has not answered yet — do NOT type the secret", retryHint, targets[0].peekHint}
			}
			return finish(targets[0], output.Envelope{Status: "timeout", Next: next})
		}
		time.Sleep(waitPoll)
	}
}

// waitNames splits the positional arguments into terminal names, accepting both
// separators an agent is likely to reach for ("wait a b c" and "wait a,b,c").
// Duplicates are dropped rather than rejected: waiting on one terminal twice is
// harmless, but arming two targets on it would poll it twice per tick. Pure:
// unit-tested directly.
func waitNames(pos []string) ([]string, error) {
	seen := map[string]bool{}
	var names []string
	for _, arg := range pos {
		for _, n := range strings.Split(arg, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, errors.New("pairmux ls")
	}
	return names, nil
}

// armWaitTarget resolves one terminal and records the baselines its conditions
// are measured against. failed reports that the whole wait must abort with rc:
// a name that does not resolve is the caller's mistake, not a condition that
// might yet come true on the others.
func (c *Ctx) armWaitTarget(name string, human, doneFlag, noteFlag bool) (t *waitTarget, rc int, failed bool) {
	term, err := state.ResolveAt(c.Tmux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, c.noTerminal(name), true
		}
		return nil, c.tmuxErr(err), true
	}
	if !term.Alive {
		return nil, c.fail(output.CodeDead, fmt.Sprintf("terminal %q is dead", name), "pairmux new"), true
	}
	program := term.Meta.Shell == ""
	if doneFlag && program {
		return nil, c.fail(output.CodeBadArgs, "this terminal runs a program, not a shell",
			fmt.Sprintf("--done needs shell completion marks; use pairmux wait %s --pattern RE or --idle 800", name)), true
	}
	j, err := journal.Open(term.Dir)
	if err != nil {
		return nil, c.fail(output.CodeInternal, err.Error(), ""), true
	}

	t = &waitTarget{
		name: name, term: term, j: j,
		baseSize: j.Size(),
		peekHint: fmt.Sprintf("pairmux peek %s", name),
	}
	if evs, err := j.Events(); err == nil {
		t.baseEvents = len(evs)
	}

	// Completion baseline. Nothing writes cmd_end while wait blocks — settlement
	// is lazy and wait is read-only — so "the command finished" can only be read
	// from the completion marks in raw.log, never from the event log. That is also
	// why a cmd_start with no cmd_end is no proof a command is still running: one
	// that finished during an earlier wait stays pending until the next run
	// records its end. Derive the real status before trusting the event.
	startStatus, _ := detect.ClassifyPending(j, deriveTerminalStatus(j, term.Alive, term.Mode, program), term.Mode, term.Meta.Tty)
	pending, hasPending, _ := j.PendingCmd()
	inFlight := hasPending && (startStatus == core.StatusRunning || startStatus == core.StatusAwaitingInput)

	// The two conditions want opposite things from an unsettled completion.
	// --done subscribes to a future one, so on an idle terminal it watches the
	// next command rather than being handed a result older than the subscription.
	// --human watches the pending command either way: a handoff exists because the
	// agent is blocked on that command, and a human who answers the prompt in the
	// pane leaves no note and no event behind, so the completion mark is the only
	// evidence there is. An already-present one is evidence the agent has not seen
	// yet — the pre-existing-note rule says such a signal resolves the wait
	// rather than being ignored, and an agent that hands off, does other work, and
	// only then waits would otherwise be back to blocking for nothing.
	//
	// --note arms neither. It is the plain "something signalled" wait, and a
	// completion it did not ask about is not that signal.
	watchPending := inFlight || (human && hasPending)
	if doneFlag || (human && hasPending) {
		t.completionFrom, t.completionFull = t.baseSize, fmt.Sprintf("pairmux log %s --range 1:end", name)
		if watchPending {
			t.completionFrom, t.completionEcho, t.completionStart = pending.Offset, pending.Text, pending.TS
			t.completionFull = fmt.Sprintf("pairmux log %s --cmd %d", name, pending.CmdID)
			if term.Mode == core.ModeSentinel {
				t.completionEcho += shellhooks.SentinelSuffix(term.Meta.Shell)
			}
		}
		t.completion = detect.NewCompletionWatcher(t.completionFrom, term.Mode)
	}

	// What a handoff is actually waiting for is the human finishing, not the
	// command finishing: answering the password of a five-minute migration ends
	// the handoff at second two. So a --human wait armed at a prompt resolves as
	// soon as the terminal is visibly moving again — output has appeared past the
	// prompt and the last line is no longer prompt-shaped. Completion stays armed
	// underneath for the cases this cannot see (a program that prints nothing
	// after the answer, or a command that had already finished).
	//
	// The prompt is judged without Classify's settle gate (see ClassifyPending).
	// A handoff wait is armed *because* the caller just found the question, and
	// the way it finds it — `wait --pattern`, which returns the instant the
	// prompt is printed — lands inside that gate every time. Requiring the
	// terminal to have been quiet first therefore left the watch unarmed for
	// precisely the flow the handoff exists for: the wait then had nothing to
	// resolve on but a note, and ran out its full deadline while the human
	// answered in the pane.
	if human && startStatus == core.StatusAwaitingInput {
		t.armAnswered()
	}
	return t, 0, false
}

// armAnswered starts watching for the handoff prompt to be answered, measured
// from the journal's current end. The signal is a line terminated after this
// point, so a watch armed late must take a fresh baseline rather than inherit
// one with output — and therefore a newline — already past it.
func (t *waitTarget) armAnswered() {
	t.watchAnswered = true
	t.answerScan = t.j.Size()
	t.answerSubmitted = false
}

// waitPollSpec is the set of conditions armed for this wait, shared by every
// target. Per-terminal state lives on waitTarget instead.
type waitPollSpec struct {
	patternRE *regexp.Regexp
	waitNote  bool
	human     bool
	waitIdle  bool
	idleFor   time.Duration
}

// pollWaitTarget evaluates one tick of every armed condition against one
// terminal. done reports that the wait is over and env is its result; failed
// reports an error that must abort the whole wait with rc.
func (c *Ctx) pollWaitTarget(t *waitTarget, spec waitPollSpec) (env output.Envelope, done bool, rc int, failed bool) {
	if spec.patternRE != nil {
		if data, err := t.j.ReadRange(t.baseSize, -1); err == nil && len(data) > 0 {
			if hit, ok := matchShapedPattern(data, spec.patternRE); ok {
				return output.Envelope{Status: "pattern-found", Output: hit, Next: []string{t.peekHint}}, true, 0, false
			}
		}
	}
	if spec.waitNote {
		if evs, err := t.j.Events(); err == nil {
			if note, ok := newNote(evs, t.baseEvents); ok {
				return noteEnvelope(t, note, spec.human), true, 0, false
			}
		}
	}
	if t.completion != nil {
		res, hit, err := t.completion.Poll(t.j)
		if err != nil {
			return output.Envelope{}, false, c.fail(output.CodeInternal, err.Error(), ""), true
		}
		if hit {
			return completionEnvelope(t.j, res, t.completionFrom, t.completionEcho, t.completionFull,
				t.completionStart, t.peekHint, !spec.human), true, 0, false
		}
	}
	// A handoff need not have its prompt on screen when the wait is armed: an
	// agent may hand off while the command is still working its way to the
	// question, and a `--pattern` wait can hand back a line the terminal has not
	// finished being at yet. So the arming decision is re-taken every poll until
	// it is made — cheaply, from the wording alone; the discipline-only prompts
	// arm from the state check below, which computes the same verdict once a
	// second anyway. Without this a wait armed a moment too early watches for the
	// answer to a question it never saw, and can only time out.
	if spec.human && !t.watchAnswered {
		if _, prompting := detect.PromptPending(t.j); prompting {
			t.armAnswered()
		}
	}
	// Keystrokes echo into the journal one at a time, so "output appeared" on
	// its own reads a human halfway through typing as a human who has finished.
	// What separates the two is the newline their Enter produces — either echoed
	// by the tty or written by the program as it accepts the line — so the line
	// the prompt sat on must be terminated before any verdict is possible.
	if t.watchAnswered && !t.answerSubmitted {
		if size := t.j.Size(); size > t.answerScan {
			if data, err := t.j.ReadRange(t.answerScan, size); err == nil && len(data) > 0 {
				t.answerScan += int64(len(data))
				t.answerSubmitted = bytes.IndexByte(data, '\n') >= 0
			}
		}
	}
	if t.watchAnswered && t.answerSubmitted {
		if _, stillPrompting := detect.PromptPending(t.j); stillPrompting {
			// Either the prompt is still the last thing on screen, or the program
			// rejected the answer and asked again. Both mean the human is not done.
			t.answeredAt = time.Time{}
		} else {
			if t.answeredAt.IsZero() {
				t.answeredAt = time.Now()
			}
			// Hold the verdict briefly: a rejected answer echoes first and the
			// re-prompt lands a moment later, and reporting "moving again" in that
			// gap would send the agent back round the handoff for nothing.
			if time.Since(t.answeredAt) >= answeredSettle {
				return c.answeredEnvelope(t)
			}
		}
	}

	stateCheckDue := spec.waitIdle || time.Since(t.lastStateCheck) >= time.Second
	if stateCheckDue && journalQuiet(t.j, spec.idleFor) {
		t.lastStateCheck = time.Now()
		// Liveness can change while wait blocks. Refresh it only once output
		// is quiet, then distinguish true idle from a quiet running command,
		// an input prompt, or a pane that died during the wait.
		current, err := state.ResolveAt(c.Tmux, c.StateDir, t.name)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return output.Envelope{Status: string(core.StatusDead), Output: waitCurrentTail(t.j), Next: []string{
					fmt.Sprintf("pairmux log %s", t.name), "pairmux new",
				}}, true, 0, false
			}
			return output.Envelope{}, false, c.tmuxErr(err), true
		}
		status, prompt, terminal := terminalStatusAfterQuiet(t.j, current.Alive, t.term.Mode,
			current.Meta.Shell == "", spec.idleFor, t.term.Meta.Tty)
		if spec.human && !t.watchAnswered && status == core.StatusAwaitingInput {
			// A prompt only the terminal's own discipline can identify (echo off in
			// a locale the wording rules miss) is invisible to the check above.
			t.armAnswered()
		}
		// awaiting-input is the reason a handoff exists, never its outcome:
		// returning it to an agent that is already handing off only sends it
		// round the same loop. --human waits for the prompt to be answered.
		if handoffPrompt := spec.human && status == core.StatusAwaitingInput; terminal &&
			(spec.waitIdle || status == core.StatusDead) && !handoffPrompt {
			next := peekNext(t.name, status)
			if status == core.StatusAwaitingInput {
				next = awaitingNext(t.name, prompt)
			}
			body := ""
			switch status {
			case core.StatusAwaitingInput:
				body = prompt.Line
			case core.StatusDead:
				body = waitCurrentTail(t.j)
			}
			return output.Envelope{Status: string(status), Output: body, Next: next}, true, 0, false
		}
	}
	return output.Envelope{}, false, 0, false
}

// noteEnvelope renders the note condition firing. --human calls it "human-done"
// because that is what it has meant since handoffs existed; --note on its own
// reports what actually happened, which need not have been a human at all — a
// sub-agent's own stop hook leaves notes through the same command.
func noteEnvelope(t *waitTarget, note string, human bool) output.Envelope {
	status := "note"
	if human {
		status = "human-done"
	}
	return output.Envelope{Status: status, Output: note, Next: []string{t.peekHint}}
}

// retryFlags rebuilds the condition flags of the wait being retried, in the
// order the usage line lists them. Only conditions the caller actually asked for
// are reproduced — an unrequested --idle would arm a condition the original wait
// did not have. Pure: unit-tested directly.
func retryFlags(seen map[string]bool, idleS, patternS string, human, done, note, notify bool) []string {
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
	if note {
		flags = append(flags, "--note")
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
func waitRetryHint(names []string, flags []string, timeout time.Duration) string {
	parts := append([]string{"pairmux", "wait", strings.Join(names, " ")}, flags...)
	parts = append(parts, "--timeout", timeout.String())
	return strings.Join(parts, " ")
}

// answeredEnvelope reports a handoff whose prompt has been answered: the
// terminal is moving again, which is the news the agent was blocked for. It
// re-resolves liveness because output stopping and the pane dying look
// identical from the journal, and it teaches --done — "running" is an
// invitation to keep following the command, not a dead end. Like every resolved
// handoff it carries no output (see completionEnvelope), except for a dead
// pane, where the agent needs the tail to work out what happened at all.
func (c *Ctx) answeredEnvelope(t *waitTarget) (output.Envelope, bool, int, bool) {
	alive := true
	if current, err := state.ResolveAt(c.Tmux, c.StateDir, t.name); err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			return output.Envelope{}, false, c.tmuxErr(err), true
		}
		alive = false
	} else {
		alive = current.Alive
	}
	program := t.term.Meta.Shell == ""
	status := deriveTerminalStatus(t.j, alive, t.term.Mode, program)
	next := peekNext(t.name, status)
	body := ""
	switch status {
	case core.StatusRunning:
		// --done is the natural way to keep following the command, but a program
		// terminal emits no completion marks and wait rejects the flag outright:
		// offering it there would hand the agent a command that only errors. The
		// ssh/2FA handoff runs on exactly such a terminal, so this is not a rare
		// branch.
		follow := fmt.Sprintf("pairmux wait %s --done", t.name)
		if program {
			follow = fmt.Sprintf("pairmux wait %s --idle 800", t.name)
		}
		next = append([]string{follow}, next...)
	case core.StatusDead:
		body = waitCurrentTail(t.j)
	}
	return output.Envelope{Status: string(status), Output: body, Next: next}, true, 0, false
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

// latestUnseenNote returns the newest EvNote the agent has not yet answered:
// notes with TS after the last EvCmdEnd or EvSent, or every note when it has
// done neither. This replicates commands.go's unseenNotes definition (kept
// local per the wave split — wait.go must not couple to commands.go), reduced
// to the latest text.
//
// EvSent is what makes a repeated --human wait terminate. A terminal running a
// long-lived program is driven by send and completes no commands, so before
// send counted as an answer the pre-existing-note rule below re-delivered the
// same note on every wait, instantly, forever — an agent looping on "prompt the
// sub-agent, wait for its reply" never blocked at all. Pure: unit-tested
// directly.
func latestUnseenNote(evs []core.Event) (string, bool) {
	var acted time.Time
	for _, ev := range evs {
		switch ev.Type {
		case core.EvCmdEnd, core.EvSent:
			if ev.TS.After(acted) {
				acted = ev.TS
			}
		}
	}
	text, found := "", false
	for _, ev := range evs {
		if ev.Type == core.EvNote && (acted.IsZero() || ev.TS.After(acted)) {
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
func terminalStatusAfterQuiet(j *journal.Journal, alive bool, mode core.Mode, program bool, idle time.Duration, tty string) (core.Status, detect.Prompt, bool) {
	if !journalQuiet(j, idle) {
		return core.StatusUnknown, detect.Prompt{}, false
	}
	status, prompt := detect.Classify(j, deriveTerminalStatus(j, alive, mode, program), mode, tty)
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
