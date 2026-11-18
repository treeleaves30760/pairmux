package cli

import (
	"encoding/json"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/output"
)

const eventsUsage = "pairmux events [--tree] [--wait] [--status S,S] [--max N] [--timeout D] [--interval 1s] [--no-snapshot] [--exit-with-parent]"

// defaultEventsWait bounds events --wait when no --timeout is given, so an
// agent that forgets one is not parked forever.
const defaultEventsWait = 10 * time.Minute

// cmdEvents reports changes to the fleet: which terminals appear, disappear,
// or change state.
//
// By default it streams JSON lines (output.EventsSchemaID): a snapshot, then a
// line per change, until interrupted. That is the subscription half of the
// integration API — the CLI envelopes are the request half: a front end (a
// desktop app, a web dashboard, an editor panel) runs it as a child process,
// renders the lines, and acts through ordinary pairmux commands.
//
// With --wait it is instead a blocking call that returns one pairmux.v1
// envelope when something changes (or --max changes have, or the timeout
// expires): the fleet-wide sibling of wait, for an agent supervising other
// agents — "tell me when any of my workers needs me".
//
// Output activity alone (last_activity) is never a change; it would turn the
// stream into a firehose. Read peek or log for content.
//
// --exit-with-parent ends it once the process that started it is gone. A
// stream notices a dead reader only on its next write, so with a quiet fleet
// an orphaned stream would otherwise poll forever. It is opt-in because the
// opposite is also a use: nohup pairmux events > file & must outlive its shell.
func (c *Ctx) cmdEvents(args []string) int {
	var tree, noSnap, waitMode, withParent bool
	var intervalS, maxS, timeoutS, statusS string
	if _, err := parseFlags(args, flagSpec{
		bools: map[string]*bool{"tree": &tree, "r": &tree, "no-snapshot": &noSnap, "wait": &waitMode, "exit-with-parent": &withParent},
		vals:  map[string]*string{"interval": &intervalS, "max": &maxS, "timeout": &timeoutS, "status": &statusS},
	}); err != nil {
		return c.usage(eventsUsage, err.Error())
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	interval := time.Second
	if intervalS != "" {
		d, err := parseInterval(intervalS)
		if err != nil {
			return c.usage(eventsUsage, "bad --interval: "+err.Error())
		}
		interval = d
	}
	maxEv := 0
	if waitMode {
		maxEv = 1
	}
	if maxS != "" {
		n, err := strconv.Atoi(maxS)
		if err != nil || n < 1 {
			return c.usage(eventsUsage, "--max takes a positive count of changes")
		}
		maxEv = n
	}
	// --status keeps only changes that leave a terminal in one of the given
	// states (removals always pass): "tell me when a worker needs me or dies".
	only, statusS, ok := parseStatusFilter(statusS)
	if !ok {
		return c.usage(eventsUsage, "--status takes running, idle, awaiting-input and/or dead, comma-separated")
	}
	var timeout time.Duration
	if timeoutS != "" {
		d, err := time.ParseDuration(timeoutS)
		if err != nil || d <= 0 {
			return c.usage(eventsUsage, "bad --timeout: want a positive duration such as 30s or 10m")
		}
		timeout = d
	} else if waitMode {
		timeout = defaultEventsWait
	}

	// Armed before the first listing, so a parent that dies while it runs is
	// still caught on the first tick.
	parentGone := func() bool { return false }
	if withParent {
		gone, stop, err := watchParent()
		if err != nil {
			return c.fail(output.CodeInternal, err.Error(), "run it without --exit-with-parent and stop it yourself")
		}
		defer stop()
		parentGone = gone
	}

	nodes, err := c.walkTree(tree)
	if err != nil {
		return c.muxErr(err)
	}
	prev, order := rowsByPath(nodes)

	// Where events go: lines on stdout, or collected into one envelope.
	var collected []output.Event
	seq := 0
	enc := json.NewEncoder(c.Stdout)
	send := func(e output.Event) bool {
		seq++
		e.TS = time.Now().UTC().Format(time.RFC3339Nano)
		if waitMode {
			collected = append(collected, e)
			return true
		}
		e.Schema, e.Seq = output.EventsSchemaID, seq
		// A failed write ends the stream. On Unix a closed stdout pipe never
		// gets here: the Go runtime raises SIGPIPE on that write and the
		// process exits by signal (141 in a shell). This path covers writers
		// that report an error instead, such as a non-stdout writer or Windows.
		return enc.Encode(e) == nil
	}
	finish := func(reason string) int {
		if reason == endParentExited {
			// Nobody may be reading any more: the goodbye is best effort, and
			// a closed pipe must not turn a clean exit into death by SIGPIPE.
			tolerateClosedStdout()
		}
		if !waitMode {
			send(output.Event{Type: "end", Reason: reason})
			return 0
		}
		return c.emitFleetChanges(collected, tree, statusS, timeout, reason)
	}

	if !noSnap && !waitMode {
		rows := make([]output.TerminalRow, 0, len(order))
		for _, p := range order {
			rows = append(rows, prev[p])
		}
		if !send(output.Event{Type: "snapshot", Terminals: rows}) {
			return 0
		}
	}

	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	changes := 0
	for {
		select {
		case <-sig:
			if waitMode {
				return finish("interrupted")
			}
			return 0
		case <-deadline:
			return finish("timeout")
		case <-tick.C:
		}
		if parentGone() {
			return finish(endParentExited)
		}
		nodes, err := c.walkTree(tree)
		if err != nil {
			continue // a backend hiccup is not a fleet change; try again next tick
		}
		cur, curOrder := rowsByPath(nodes)
		for _, ev := range diffFleet(prev, cur, curOrder) {
			if len(only) > 0 && ev.Terminal != nil && (!only[ev.Terminal.Status] || ev.Type == "changed" && ev.From == "") {
				continue
			}
			if !send(ev) {
				return 0
			}
			if changes++; maxEv > 0 && changes >= maxEv {
				return finish("max")
			}
		}
		prev = cur
	}
}

// parseStatusFilter reads --status: the set of statuses to keep, and its
// canonical spelling for the keep-watching next step — one shell word even when
// the caller wrote "idle, dead". ok is false for a status that does not exist.
func parseStatusFilter(s string) (only map[string]bool, canon string, ok bool) {
	only = map[string]bool{}
	var statuses []string
	for _, st := range strings.Split(s, ",") {
		st = strings.TrimSpace(st)
		if st == "" || only[st] {
			continue
		}
		switch core.Status(st) {
		case core.StatusRunning, core.StatusIdle, core.StatusAwaitingInput, core.StatusDead:
			only[st] = true
			statuses = append(statuses, st)
		default:
			return nil, "", false
		}
	}
	return only, strings.Join(statuses, ","), true
}

func rowsByPath(nodes []treeNode) (map[string]output.TerminalRow, []string) {
	rows := make(map[string]output.TerminalRow, len(nodes))
	order := make([]string, 0, len(nodes))
	for _, n := range nodes {
		r := n.row(true)
		rows[r.Path] = r
		order = append(order, r.Path)
	}
	return rows, order
}

// diffFleet lists what changed between two listings: additions and changes in
// the new listing's order, then removals.
func diffFleet(prev, cur map[string]output.TerminalRow, order []string) []output.Event {
	var out []output.Event
	for _, p := range order {
		r := cur[p]
		old, seen := prev[p]
		if !seen {
			out = append(out, output.Event{Type: "added", Path: p, Terminal: &r})
			continue
		}
		diff := rowChanges(old, r)
		if len(diff) == 0 {
			continue
		}
		ev := output.Event{Type: "changed", Path: p, Terminal: &r, Changes: diff}
		if old.Status != r.Status {
			ev.From = old.Status
		}
		out = append(out, ev)
	}
	var gone []string
	for p := range prev {
		if _, ok := cur[p]; !ok {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	for _, p := range gone {
		out = append(out, output.Event{Type: "removed", Path: p})
	}
	return out
}

// rowChanges lists the fields of a row that a subscriber acts on.
func rowChanges(a, b output.TerminalRow) []string {
	var out []string
	if a.Status != b.Status {
		out = append(out, "status")
	}
	if a.CurrentCmd != b.CurrentCmd {
		out = append(out, "current_cmd")
	}
	if a.LockHolder != b.LockHolder {
		out = append(out, "lock_holder")
	}
	if a.Notes != b.Notes {
		out = append(out, "notes")
	}
	if a.ChildSocket != b.ChildSocket {
		out = append(out, "child_socket")
	}
	if a.Mode != b.Mode {
		out = append(out, "mode")
	}
	return out
}

// endParentExited is the end reason (and, for --wait, the stop cause) when
// --exit-with-parent saw the starting process go away.
const endParentExited = "parent-exited"

// emitFleetChanges is events --wait's reply: what changed, and what to do
// about each change. reason is why the wait stopped: max, timeout,
// interrupted (a signal) or parent-exited. A wait stopped from outside before
// anything changed is interrupted, not timeout: its deadline never arrived, so
// the timeout advice (wait longer) would be wrong.
func (c *Ctx) emitFleetChanges(evs []output.Event, tree bool, statusS string, timeout time.Duration, reason string) int {
	status := "changed"
	if len(evs) == 0 {
		status = "timeout"
		if reason == "interrupted" || reason == endParentExited {
			status = "interrupted"
		}
	}
	again := "pairmux events --wait"
	if tree {
		again += " --tree"
	}
	if statusS != "" {
		again += " --status " + statusS
	}
	var next []string
	for _, ev := range evs {
		if ev.Terminal == nil {
			continue
		}
		switch {
		case ev.Terminal.Status == string(core.StatusAwaitingInput):
			next = append(next, "pairmux peek "+ev.Path+"   # it needs input: read the prompt before answering")
		case ev.From == string(core.StatusRunning) && ev.Terminal.Status == string(core.StatusIdle):
			next = append(next, "pairmux peek "+ev.Path+"   # its command finished: read the result")
		case ev.Terminal.Status == string(core.StatusDead):
			next = append(next, "pairmux log "+ev.Path+"   # it died: read why")
		}
	}
	switch {
	case status == "timeout":
		next = append(next, again+" --timeout "+(2*timeout).String()+"   # nothing changed; keep watching")
	case status == "interrupted" && reason == endParentExited:
		next = append(next, again+"   # stopped because the process that started it exited; nothing changed before that")
	case status == "interrupted":
		next = append(next, again+"   # stopped by a signal before anything changed; run it again to keep watching")
	default:
		next = append(next, again+"   # keep watching")
	}
	return c.emit(output.Envelope{Status: status, Events: evs, Next: next})
}
