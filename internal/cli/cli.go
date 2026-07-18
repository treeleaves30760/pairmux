// Package cli wires the pairmux subcommands (new, ls, run, send, peek, log,
// kill) onto the foundation packages and renders every reply through the
// output envelope. It owns argument parsing that is deliberately forgiving —
// agents routinely misplace flags — and the agent-facing help text.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/shape"
	"github.com/treeleaves30760/pairmux/internal/state"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

// Ctx carries the per-invocation dependencies each command needs.
type Ctx struct {
	Tmux     *tmux.Client
	JSON     bool
	StateDir string
	Stdout   io.Writer
}

// Main is the process entry point behind cmd/pairmux. It resolves global flags
// and environment overrides, builds a Ctx, and dispatches. It returns the
// process exit code (0 ok, 1 pairmux error, 2 usage).
func Main(args []string) int {
	socket := os.Getenv("PAIRMUX_SOCKET")
	rest, jsonMode, socket := stripGlobals(args, false, socket)
	c := &Ctx{
		Tmux:     tmux.New(socket),
		JSON:     jsonMode,
		StateDir: state.BaseDir(),
		Stdout:   os.Stdout,
	}
	return c.dispatch(rest)
}

// dispatch routes to a subcommand handler.
func (c *Ctx) dispatch(rest []string) int {
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		c.printHelp()
		return 2
	}
	cmd, args := rest[0], rest[1:]
	switch cmd {
	case "new":
		return c.cmdNew(args)
	case "run":
		return c.cmdRun(args)
	case "peek":
		return c.cmdPeek(args)
	case "wait":
		return c.cmdWait(args)
	case "send":
		return c.cmdSend(args)
	case "log":
		return c.cmdLog(args)
	case "ls":
		return c.cmdLs(args)
	case "kill":
		return c.cmdKill(args)
	case "attach":
		return c.cmdAttach(args)
	case "watch":
		return c.cmdWatch(args)
	case "note":
		return c.cmdNote(args)
	case "doctor":
		return c.cmdDoctor(args)
	case "skill":
		return c.cmdSkill(args)
	case "version", "--version":
		return c.cmdVersion()
	case "help", "--help", "-h":
		c.printHelp()
		return 0
	default:
		return c.unknownCmd(cmd)
	}
}

// dir is the state directory for a terminal under this invocation's state root.
func (c *Ctx) dir(name string) string { return filepath.Join(c.StateDir, name) }

// emit writes a success envelope and returns exit code 0.
func (c *Ctx) emit(e output.Envelope) int {
	e.OK = true
	output.Emit(c.Stdout, c.JSON, e)
	return 0
}

// fail writes an E_* error envelope and returns exit code 1.
func (c *Ctx) fail(code, msg, hint string) int {
	return output.Fail(c.Stdout, c.JSON, code, msg, hint)
}

// usage reports a structural CLI misuse: a one-line usage string plus a hint,
// exit code 2. In JSON mode it still emits a parseable envelope.
func (c *Ctx) usage(oneline, hint string) int {
	e := output.Envelope{
		OK:     false,
		Status: "error",
		Error:  &output.ErrInfo{Code: output.CodeBadArgs, Message: "usage: " + oneline, Hint: hint},
	}
	if hint != "" {
		e.Next = []string{hint}
	}
	output.Emit(c.Stdout, c.JSON, e)
	return 2
}

// tmuxErr maps a raw tmux failure to an E_TMUX envelope.
func (c *Ctx) tmuxErr(err error) int {
	return c.fail(output.CodeTmux, err.Error(), "pairmux ls")
}

// noTerminal reports E_NO_TERMINAL, listing the terminals that do exist so an
// agent can correct a mistyped name.
func (c *Ctx) noTerminal(name string) int {
	names := c.existingNames()
	msg := fmt.Sprintf("no terminal %q", name)
	hint := "pairmux ls"
	if len(names) == 0 {
		msg += " (none exist)"
		hint = "pairmux new"
	} else {
		msg += "; existing: " + strings.Join(names, ", ")
	}
	return c.fail(output.CodeNoTerminal, msg, hint)
}

// existingNames returns the names of all known terminals (best effort).
func (c *Ctx) existingNames() []string {
	terms, err := state.List(c.Tmux)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(terms))
	for _, t := range terms {
		names = append(names, t.Name)
	}
	return names
}

// unknownCmd reports an unrecognized subcommand.
func (c *Ctx) unknownCmd(cmd string) int {
	if c.JSON {
		e := output.Envelope{
			OK:     false,
			Status: "error",
			Error:  &output.ErrInfo{Code: output.CodeBadArgs, Message: fmt.Sprintf("unknown command %q", cmd), Hint: "pairmux help"},
			Next:   []string{"pairmux help"},
		}
		output.Emit(c.Stdout, true, e)
		return 2
	}
	fmt.Fprintf(c.Stdout, "unknown command %q\n\n", cmd)
	c.printHelp()
	return 2
}

const helpText = `pairmux — reliable terminal primitives for AI agents on tmux

usage: pairmux [--json] [--socket S] <command> [args]

agent commands:
  new   [--name N] [--cwd D] [--cmd "..."]         create a terminal            e.g. pairmux new --name build
  run   <name> <cmd...> [--timeout 60s]            run a command, wait for it   e.g. pairmux run build "make -j4"
  peek  <name> [--screen | --tail N]               recent output + status       e.g. pairmux peek build
  wait  <name> [--idle MS|--pattern RE|--human]    block until a condition      e.g. pairmux wait build --pattern "listening"
  send  <name> [--text S] [--key K] [--enter]      send input to a program      e.g. pairmux send build --text y --enter
  log   <name> [--cmd N|--grep RE|--range A:B]     full or filtered output      e.g. pairmux log build --cmd 2
  ls                                               list terminals + status      e.g. pairmux ls
  kill  <name> | --all                             kill terminal(s), keep logs  e.g. pairmux kill build

human commands:
  attach [name]            take over the tmux session        e.g. pairmux attach build
  watch  [--interval 2s]   live dashboard (Ctrl-C quits)     e.g. pairmux watch
  note   <name> <text...>  leave a message for the agent     e.g. pairmux note build "fixed the token"
  doctor                   probe tmux + shell integration    e.g. pairmux doctor
  skill install [--target T | all] [--dry-run]  teach your agent pairmux   e.g. pairmux skill install --target all
  version                  print version

add --json anywhere for machine-readable envelopes (before or after the command).
put -- before a command that itself contains --json or --socket.
`

func (c *Ctx) printHelp() { io.WriteString(c.Stdout, helpText) }

// stripGlobals extracts --json and --socket from anywhere in args (up to a
// literal "--", which is preserved for the subcommand). Agents get flag order
// wrong, so a global may appear before or after the subcommand word; the "--"
// terminator is the escape hatch for a command that itself contains --json or
// --socket.
func stripGlobals(args []string, jsonMode bool, socket string) (rest []string, json bool, sock string) {
	json, sock = jsonMode, socket
	noMore := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if noMore {
			rest = append(rest, a)
			continue
		}
		if a == "--" {
			noMore = true
			rest = append(rest, a)
			continue
		}
		if len(a) > 2 && a[0] == '-' {
			name, val, hasVal := splitFlag(a)
			switch name {
			case "json":
				json = true
				continue
			case "socket":
				if hasVal {
					sock = val
					continue
				}
				if i+1 < len(args) {
					sock = args[i+1]
					i++
				}
				continue
			}
		}
		rest = append(rest, a)
	}
	return rest, json, sock
}

// splitFlag turns "--name" / "-name" / "--name=val" into (name, val, hasVal).
func splitFlag(a string) (name, val string, hasVal bool) {
	name = strings.TrimLeft(a, "-")
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		return name[:eq], name[eq+1:], true
	}
	return name, "", false
}

// flagSpec declares the flags a subcommand accepts. seen (when non-nil) records
// which flags were present so a caller can distinguish "unset" from "set empty".
type flagSpec struct {
	bools map[string]*bool
	vals  map[string]*string
	lists map[string]*[]string
	seen  map[string]bool
}

// parseFlags parses order-independent flags and positionals for the simple
// subcommands. Any token starting with '-' (other than a lone '-') is a flag;
// "--" ends flag parsing. An unrecognized flag is an error (a usage mistake).
func parseFlags(args []string, spec flagSpec) (pos []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			name, val, hasVal := splitFlag(a)
			switch {
			case spec.bools[name] != nil:
				*spec.bools[name] = true
			case spec.vals[name] != nil:
				if !hasVal {
					i++
					if i >= len(args) {
						return nil, fmt.Errorf("flag --%s needs a value", name)
					}
					val = args[i]
				}
				*spec.vals[name] = val
			case spec.lists[name] != nil:
				if !hasVal {
					i++
					if i >= len(args) {
						return nil, fmt.Errorf("flag --%s needs a value", name)
					}
					val = args[i]
				}
				*spec.lists[name] = append(*spec.lists[name], val)
			default:
				return nil, fmt.Errorf("unknown flag --%s", name)
			}
			if spec.seen != nil {
				spec.seen[name] = true
			}
			continue
		}
		pos = append(pos, a)
	}
	return pos, nil
}

// parseRun parses run's arguments. run is special: only the known value flags
// (--timeout/--head/--tail) are extracted, and only before a literal "--";
// every other token — including dash-leading command flags like `-n` — is a
// command token, so `run t echo -n hi` keeps the `-n`.
func parseRun(args []string) (timeout, head, tail string, pos []string, err error) {
	noMore := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !noMore && a == "--" {
			noMore = true
			continue
		}
		if !noMore && len(a) > 2 && a[0] == '-' {
			name, val, hasVal := splitFlag(a)
			if name == "timeout" || name == "head" || name == "tail" {
				if !hasVal {
					i++
					if i >= len(args) {
						return "", "", "", nil, fmt.Errorf("flag --%s needs a value", name)
					}
					val = args[i]
				}
				switch name {
				case "timeout":
					timeout = val
				case "head":
					head = val
				case "tail":
					tail = val
				}
				continue
			}
		}
		pos = append(pos, a)
	}
	return timeout, head, tail, pos, nil
}

// --- output shaping helpers (compose shape's exported steps) ---

// cleanNoTrunc runs the shape pipeline without line truncation: it resolves
// carriage returns, strips control sequences, trims trailing blanks, and drops
// the echoed command (cmd == "" skips the drop).
func cleanNoTrunc(raw []byte, cmd string) string {
	b := shape.CollapseCR(raw)
	b = shape.StripANSI(b)
	s := trimTrailingBlank(string(b))
	return shape.DropEchoedCommand(s, cmd)
}

// trimTrailingBlank drops trailing blank/whitespace-only lines.
func trimTrailingBlank(s string) string {
	lines := strings.Split(s, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[:end], "\n")
}

// lastLines keeps the final n lines of s and reports how many were dropped.
func lastLines(s string, n int) (string, int) {
	if n <= 0 || s == "" {
		return s, 0
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s, 0
	}
	omitted := len(lines) - n
	return strings.Join(lines[len(lines)-n:], "\n"), omitted
}
