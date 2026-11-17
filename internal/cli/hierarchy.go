package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// The agent hierarchy.
//
// An agent running in a pairmux terminal that itself uses pairmux builds its
// own layer: every terminal is created with PAIRMUX_SOCKET set to its child
// endpoint (state.ChildSocket), so whatever it opens lands one level down. That
// alone gives a tree — a coordinator, its workers, their sub-workers — but not a
// chain of command: nothing stopped a worker from passing --socket and typing
// into its coordinator, or into a sibling, or into the session a human is
// driving at the top.
//
// The scope closes that. Each terminal is also told its scope (the same child
// endpoint) and its owner (the endpoint that holds it). A process carrying a
// scope may address that layer and anything below it, and nothing else: control
// flows down, never up or sideways. The one upward channel is a report — a note
// on the reporter's own terminal in its owner's layer, which is how a worker
// tells its coordinator a turn is over. The top of the tree, whatever runs in
// the human's own terminal, has no scope and is addressable only by the human.
//
// This is a guardrail for cooperating agents, not a security boundary: every
// layer runs as the same user, and a process that deliberately clears its
// environment is outside it. What it reliably prevents is the realistic failure
// — an agent that copies a --socket out of a hint, a log or its own creator's
// instructions and drives a terminal it was never given.
const (
	// ScopeEnv is the endpoint a terminal's processes are confined to.
	ScopeEnv = "PAIRMUX_SCOPE"
	// OwnerSocketEnv is the endpoint that owns the terminal — where its own
	// row, journal and notes live. report writes there.
	OwnerSocketEnv = "PAIRMUX_OWNER_SOCKET"
	// NameEnv is the terminal's own name in its owner's layer.
	NameEnv = "PAIRMUX_NAME"
)

// scopeExempt lists commands that never reach a terminal, plus report, which
// is the sanctioned upward channel and checks its own target.
func scopeExempt(cmd string) bool {
	return noTerminalCommand(cmd) || cmd == "report"
}

// enforceScope refuses a command whose endpoint lies outside the caller's
// scope. It runs once, before dispatch, against the endpoint the invocation
// resolved (--socket, else PAIRMUX_SOCKET); path addressing (a/b/c) only ever
// descends from there, so it cannot leave a scope this check admitted.
func (c *Ctx) enforceScope(rest []string) (int, bool) {
	scope := os.Getenv(ScopeEnv)
	if scope == "" || len(rest) == 0 {
		return 0, false
	}
	cmd := rest[0]
	if cmd == "--" && len(rest) > 1 {
		cmd = rest[1]
	}
	if scopeExempt(cmd) {
		return 0, false
	}
	target := c.Mux.SocketName()
	if state.IsDescendantLayer(c.StateDir, c.Mux.Backend(), scope, target) {
		return 0, false
	}
	// The pre-report recipe: `pairmux --socket <owner> note <own name> "..."`.
	// It is the same upward report, spelled the long way, so it stays allowed.
	if cmd == "note" && target == os.Getenv(OwnerSocketEnv) && firstPositional(rest[1:]) == os.Getenv(NameEnv) {
		return 0, false
	}
	return c.fail(output.CodeScope,
		fmt.Sprintf("endpoint %q is outside this terminal's scope %q: an agent may drive its own layer and the layers below it, never its owner, its siblings or the top", target, scope),
		`pairmux ls --tree   # what you may drive; tell your owner with: pairmux report "..."`), true
}

// firstPositional returns the first argument that is not a flag.
func firstPositional(args []string) string {
	for _, a := range args {
		if a == "--" {
			continue
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// resolveTarget validates a terminal reference and, for a path, moves this
// invocation onto the layer it names. "worker/sub" is terminal "sub" in the
// layer worker drives; any depth works, relative to the current endpoint. A
// coordinator can therefore reach a sub-agent directly without spelling out
// derived endpoint names, and since a path only descends, a scoped caller
// stays inside its scope.
//
// On success *name is the leaf and replies are rewritten to show the path, so
// the next steps they suggest still address the same terminal.
func (c *Ctx) resolveTarget(name *string) (int, bool) {
	ref := *name
	if !strings.Contains(ref, "/") {
		return c.rejectInvalidTerminalName(ref)
	}
	// Strict: a leading "/" would read an absolute filesystem path as a
	// terminal path, and empty segments hide typos.
	segs := strings.Split(ref, "/")
	for _, s := range segs {
		if !state.ValidName(s) {
			return c.fail(output.CodeBadArgs, fmt.Sprintf("invalid name %q in terminal path %q", s, ref),
				"paths are terminal names joined by /, e.g. worker/tests — "+terminalNameHint), true
		}
	}
	socket := c.baseSocket()
	for _, s := range segs[:len(segs)-1] {
		socket = state.ChildSocket(socket, s)
	}
	leaf := segs[len(segs)-1]
	if c.Mux.SocketName() != socket {
		if c.aliases != nil {
			return c.fail(output.CodeBadArgs,
				fmt.Sprintf("%q is on another layer than the other terminals named", ref),
				"name terminals of one layer per call"), true
		}
		c.Mux = c.layer(socket)
	}
	if c.aliases == nil {
		c.aliases = map[string]string{}
	}
	c.aliases[leaf] = strings.Join(segs, "/")
	*name = leaf
	return 0, false
}

// baseSocket is the endpoint the invocation started on, before any path moved
// it to another layer.
func (c *Ctx) baseSocket() string {
	if c.origSocket != "" {
		return c.origSocket
	}
	c.origSocket = c.Mux.SocketName()
	return c.origSocket
}

// rewriteAliases makes a reply produced on a path's leaf layer read in the
// caller's terms: the terminal field and the terminal argument of each
// suggested pairmux command become the path again.
func (c *Ctx) rewriteAliases(e *output.Envelope) {
	if len(c.aliases) == 0 {
		return
	}
	if p, ok := c.aliases[e.Terminal]; ok {
		e.Terminal = p
	}
	for i, line := range e.Next {
		e.Next[i] = c.rewriteCommand(line)
	}
	if e.Error != nil {
		e.Error.Hint = c.rewriteCommand(e.Error.Hint)
	}
	if e.Truncated != nil {
		e.Truncated.GetFull = c.rewriteCommand(e.Truncated.GetFull)
	}
}

// rewriteCommand replaces the terminal argument of a "pairmux <verb> <name>"
// hint (a comma list counts) with its path. Anything else is left alone.
func (c *Ctx) rewriteCommand(line string) string {
	if !strings.HasPrefix(line, "pairmux ") {
		return line
	}
	toks := strings.Split(line, " ")
	// toks[0] is pairmux; skip global flags to the verb, then its argument.
	i := 1
	for i < len(toks) && strings.HasPrefix(toks[i], "-") {
		i++
	}
	i++ // the verb
	if i >= len(toks) {
		return line
	}
	parts := strings.Split(toks[i], ",")
	changed := false
	for k, p := range parts {
		if path, ok := c.aliases[p]; ok {
			parts[k] = path
			changed = true
		}
	}
	if !changed {
		return line
	}
	toks[i] = strings.Join(parts, ",")
	return strings.Join(toks, " ")
}

// cmdReport is the upward channel: it leaves a note on the caller's own
// terminal in the layer that owns it, where whoever created that terminal reads
// it in its next envelope or wakes on it with `wait <name> --note`. It is the
// only way a scoped agent addresses anything above its own layer, and all it
// can do there is talk about itself.
func (c *Ctx) cmdReport(args []string) int {
	const usageLine = `pairmux report <text...>`
	pos, err := parseFlags(args, flagSpec{})
	if err != nil {
		return c.usage(usageLine, err.Error())
	}
	if len(pos) == 0 {
		return c.usage(usageLine, `pairmux report "tests pass; ready for review"`)
	}
	owner, name := os.Getenv(OwnerSocketEnv), os.Getenv(NameEnv)
	if owner == "" || name == "" {
		return c.fail(output.CodeBadArgs,
			"report only works inside a pairmux terminal: there is no owner to report to here",
			`pairmux note <name> "..."   # from outside, address the terminal directly`)
	}
	c.Mux = c.layer(owner)
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	c.reporting = true
	return c.cmdNote(append([]string{"--", name}, pos...))
}
