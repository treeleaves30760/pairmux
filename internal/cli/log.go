package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/shellhooks"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// cmdLog and its selectors: reading a terminal's retained output history.

// cmdLog returns a command's full output (--cmd N), grep matches (--grep RE),
// a shaped-line range (--range A:B or A:end), or the journal tail (no flag).
func (c *Ctx) cmdLog(args []string) int {
	const usageLine = "pairmux log <name> [--cmd N | --grep RE | --range A:B|A:end]"
	var cmdS, grepS, rangeS string
	seen := map[string]bool{}
	pos, err := parseFlags(args, flagSpec{
		vals: map[string]*string{"cmd": &cmdS, "grep": &grepS, "range": &rangeS},
		seen: seen,
	})
	if err != nil {
		return c.usage(usageLine, err.Error())
	}
	if len(pos) == 0 {
		return c.usage(usageLine, "pairmux ls")
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	name := pos[0]
	if rc, rejected := c.resolveTarget(&name); rejected {
		return rc
	}
	selected := 0
	for _, f := range []string{"cmd", "grep", "range"} {
		if seen[f] {
			selected++
		}
	}
	if selected > 1 {
		return c.usage(usageLine, "--cmd, --grep and --range are mutually exclusive")
	}

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

	switch {
	case seen["cmd"]:
		id, err := strconv.Atoi(cmdS)
		if err != nil {
			return c.fail(output.CodeBadArgs, fmt.Sprintf("bad --cmd id %q", cmdS), "pairmux log "+name)
		}
		return c.logCmd(name, j, term.Mode, shellhooks.SentinelHasBoundary(term.Meta.Shell), id)
	case seen["grep"]:
		return c.logGrep(name, j, term.Mode, grepS)
	case seen["range"]:
		return c.logRange(name, j, term.Mode, rangeS)
	}

	raw, start, err := j.TailBytes(shapedViewCap)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	body, omitted := lastLines(cleanNoTrunc(raw, ""), 500)
	env := output.Envelope{Status: "ok", Terminal: name, Mode: string(term.Mode), Output: body}
	if omitted > 0 || start > 0 {
		env.Truncated = &output.TruncInfo{
			OmittedLines: omitted,
			OmittedBytes: start,
			GetFull:      fmt.Sprintf("pairmux log %s --range 1:end", name),
		}
	}
	return c.emit(env)
}

// shapedJournal returns the cleaned complete journal. Explicit log selectors
// opt into this potentially large read; bounded peek/default-log views use
// TailBytes instead.
func shapedJournal(j *journal.Journal) (string, error) {
	raw, err := j.ReadRange(0, -1)
	if err != nil {
		return "", err
	}
	return cleanNoTrunc(raw, ""), nil
}

// logGrep emits every shaped-journal line matching pattern, each prefixed with
// its 1-based line number.
func (c *Ctx) logGrep(name string, j *journal.Journal, mode core.Mode, pattern string) int {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return c.fail(output.CodeBadArgs, "bad --grep: "+err.Error(),
			`RE2 syntax, e.g. pairmux log `+name+` --grep "error|panic"`)
	}
	text, err := shapedJournal(j)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	var matches []string
	for i, ln := range strings.Split(text, "\n") {
		if !re.MatchString(ln) {
			continue
		}
		matches = append(matches, fmt.Sprintf("%d:%s", i+1, ln))
	}
	return c.emit(output.Envelope{
		Status: "ok", Terminal: name, Mode: string(mode), Output: strings.Join(matches, "\n"),
	})
}

// logRange emits the 1-based inclusive shaped-line range A:B (or A:end).
func (c *Ctx) logRange(name string, j *journal.Journal, mode core.Mode, spec string) int {
	a, b, err := parseRange(spec)
	if err != nil {
		return c.fail(output.CodeBadArgs, "bad --range: "+err.Error(), "pairmux log "+name+" --range 100:160")
	}
	text, err := shapedJournal(j)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	lines := strings.Split(text, "\n")
	if b < 0 || b > len(lines) {
		b = len(lines)
	}
	var body string
	if a <= b {
		body = strings.Join(lines[a-1:b], "\n")
	}
	return c.emit(output.Envelope{Status: "ok", Terminal: name, Mode: string(mode), Output: body})
}

// parseRange parses --range's A:B or A:end as a 1-based inclusive line range.
func parseRange(s string) (a, b int, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want A:B or A:end (1-based, inclusive)")
	}
	a, errA := strconv.Atoi(strings.TrimSpace(parts[0]))
	end := strings.TrimSpace(parts[1])
	b = -1
	var errB error
	if end != "end" {
		b, errB = strconv.Atoi(end)
	}
	if errA != nil || errB != nil {
		return 0, 0, fmt.Errorf("want line range A:B or A:end")
	}
	if a < 1 || (b >= 0 && b < a) {
		return 0, 0, fmt.Errorf("want 1 <= A <= B")
	}
	return a, b, nil
}

// logCmd renders the complete output region of one explicitly selected
// command. Unlike bounded peek/default-log views, an explicit command lookup
// never silently byte-caps its output.
func (c *Ctx) logCmd(name string, j *journal.Journal, mode core.Mode, bounded bool, id int) int {
	events, err := j.Events()
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	var startEv *core.Event
	var endOff int64 = -1
	for i := range events {
		switch {
		case events[i].Type == core.EvCmdStart && events[i].CmdID == id:
			ev := events[i]
			startEv = &ev
		case events[i].Type == core.EvCmdEnd && events[i].CmdID == id:
			endOff = events[i].Offset
		}
	}
	if startEv == nil {
		return c.fail(output.CodeBadArgs, fmt.Sprintf("no command %d for terminal %q", id, name), "pairmux log "+name)
	}
	from := startEv.Offset
	to := endOff
	if to < 0 {
		if mk, ok := scanCompletion(j, from, mode, bounded); ok {
			to = mk
		}
	}

	raw, err := j.ReadRange(from, to)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	return c.emit(output.Envelope{
		Status: "ok", Terminal: name, Mode: string(mode), Output: cleanNoTrunc(raw, startEv.Text),
	})
}
