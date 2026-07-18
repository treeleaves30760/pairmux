package cli

import (
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
	"github.com/treeleaves30760/pairmux/internal/state"
)

const waitPoll = 200 * time.Millisecond

// cmdWait blocks until a terminal satisfies a condition: new output matching
// --pattern, a new human note event (--human), or quiescence (--idle MS, the
// default). It is read-only — it records no events and takes no lock — so a
// human and an agent can both wait on the same terminal.
func (c *Ctx) cmdWait(args []string) int {
	const usageLine = `pairmux wait <name> [--idle MS] [--pattern RE] [--human] [--notify] [--timeout 300s]`

	var idleS, patternS, timeoutS string
	var human, notifyHuman bool
	seen := map[string]bool{}
	pos, err := parseFlags(args, flagSpec{
		bools: map[string]*bool{"human": &human, "notify": &notifyHuman},
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
	// --pattern/--human. First condition satisfied wins.
	waitIdle := (!seen["pattern"] && !seen["human"]) || seen["idle"]

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

	var extraNext []string
	finish := func(e output.Envelope) int {
		e.Terminal = name
		e.Mode = string(term.Mode)
		if len(extraNext) > 0 {
			e.Next = append(append([]string{}, extraNext...), e.Next...)
		}
		return c.emit(e)
	}
	peekHint := fmt.Sprintf("pairmux peek %s", name)

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
	var lastStateCheck time.Time
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
			if status, prompt, terminal := terminalStatusAfterQuiet(j, current.Alive, term.Mode, idleFor); terminal &&
				(waitIdle || status == core.StatusDead) {
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
			return finish(output.Envelope{Status: "timeout", Next: []string{
				peekHint, fmt.Sprintf("pairmux wait %s --timeout %v", name, 2*timeout),
			}})
		}
		time.Sleep(waitPoll)
	}
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
func terminalStatusAfterQuiet(j *journal.Journal, alive bool, mode core.Mode, idle time.Duration) (core.Status, string, bool) {
	if !journalQuiet(j, idle) {
		return core.StatusUnknown, "", false
	}
	status, prompt := detect.Refine(j, detect.DeriveStatus(j, alive, mode), mode)
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
