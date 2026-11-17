package cli

import (
	"errors"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// Notes: messages a human (or another agent) leaves for the agent driving a
// terminal, and when that agent counts as having seen them.

// cmdNote records a human message for the agent driving a terminal. The agent
// sees it in the notes field of its next run/peek envelope (or via wait --human).
func (c *Ctx) cmdNote(args []string) int {
	pos, err := parseFlags(args, flagSpec{})
	if err != nil {
		return c.usage("pairmux note <name> <text...>", err.Error())
	}
	if len(pos) < 2 {
		return c.usage("pairmux note <name> <text...>", `pairmux note build "use the staging token"`)
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	name := pos[0]
	if rc, rejected := c.resolveTarget(&name); rejected {
		return rc
	}
	text := strings.Join(pos[1:], " ")

	term, err := state.ResolveAt(c.Mux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.muxErr(err)
	}
	j, err := journal.Open(term.Dir)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	if err := j.AppendEvent(core.Event{Type: core.EvNote, Text: text}); err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	return c.emit(output.Envelope{
		Status: "noted", Terminal: name,
		Next: []string{"the agent sees this note in its next run/peek, or as output from pairmux wait " + name + " --human"},
	})
}

// lastActedOn returns when the agent last did something to this terminal:
// finished a command, or delivered input to one. Notes older than that have
// been answered.
//
// EvSent belongs here for the terminals that never finish a command at all. A
// pane holding a long-lived program — a pager, an editor, another agent's
// terminal UI — is driven entirely by send, so its last EvCmdEnd is from
// whatever launched the program, if there ever was one. Without send as a
// boundary a note stays unseen for the life of the terminal, and every wait
// after the first returns it again instantly. Pure: unit-tested directly.
func lastActedOn(evs []core.Event) time.Time {
	var last time.Time
	for _, ev := range evs {
		switch ev.Type {
		case core.EvCmdEnd, core.EvSent:
			if ev.TS.After(last) {
				last = ev.TS
			}
		}
	}
	return last
}

// unseenNotes returns the texts of EvNote events recorded after the agent last
// acted on the terminal — human messages it has not had a chance to see. When
// it has never acted, every note is unseen.
func unseenNotes(evs []core.Event) []string {
	acted := lastActedOn(evs)
	var notes []string
	for _, ev := range evs {
		if ev.Type == core.EvNote && (acted.IsZero() || ev.TS.After(acted)) {
			notes = append(notes, ev.Text)
		}
	}
	return notes
}

// readUnseenNotes is unseenNotes over the journal's current event log (best
// effort: an unreadable log means no notes). Used by the read-only commands
// (peek, ls); cmdRun instead snapshots via notesBaseline before it writes.
func readUnseenNotes(j *journal.Journal) []string {
	evs, err := j.Events()
	if err != nil {
		return nil
	}
	return unseenNotes(evs)
}

// notesBaseline returns the current event count and the unseen notes as of
// now. cmdRun calls it before its settlement scan appends anything, so a note
// that arrived while the previous command was unsettled cannot be eclipsed by
// the settlement EvCmdEnd's fresh timestamp.
func notesBaseline(j *journal.Journal) (eventCount int, notes []string) {
	evs, err := j.Events()
	if err != nil {
		return 0, nil
	}
	return len(evs), unseenNotes(evs)
}

// notesArrivedSince returns the texts of EvNote events appended after the
// first baseline events — notes left while the current invocation was working.
// Position-based, so it composes with notesBaseline without double counting.
func notesArrivedSince(j *journal.Journal, baseline int) []string {
	evs, err := j.Events()
	if err != nil || baseline >= len(evs) {
		return nil
	}
	if baseline < 0 {
		baseline = 0
	}
	var out []string
	for _, ev := range evs[baseline:] {
		if ev.Type == core.EvNote {
			out = append(out, ev.Text)
		}
	}
	return out
}
