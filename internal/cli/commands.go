package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/treeleaves30760/pairmux/internal/backend"
	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/detect"
	"github.com/treeleaves30760/pairmux/internal/flock"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/shape"
	"github.com/treeleaves30760/pairmux/internal/shellhooks"
	"github.com/treeleaves30760/pairmux/internal/state"
	"github.com/treeleaves30760/pairmux/internal/version"
)

// cmdNew creates a terminal: prepare the shell shim, open a tmux window, wire
// pipe-pane into the journal, and wait for the shell to become interactive.
func (c *Ctx) cmdNew(args []string) int {
	var name, cwd, cmdOverride string
	pos, err := parseFlags(args, flagSpec{vals: map[string]*string{"name": &name, "cwd": &cwd, "cmd": &cmdOverride}})
	if err != nil {
		return c.usage(`pairmux new [--name N] [--cwd D] [--cmd "..."]`, err.Error())
	}
	if len(pos) > 0 {
		return c.usage(`pairmux new [--name N] [--cwd D] [--cmd "..."]`, "unexpected argument "+pos[0])
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}

	var releaseName func()
	if name == "" {
		existing := c.existingNamesScoped()
		for {
			name = state.NextAutoName(existing)
			releaseName, err = state.AcquireNameReservation(c.namespaceDir(), name)
			if errors.Is(err, state.ErrNameReserved) {
				existing = append(existing, name)
				continue
			}
			break
		}
	} else {
		if rc, rejected := c.resolveTarget(&name); rejected {
			return rc
		}
		releaseName, err = state.AcquireNameReservation(c.namespaceDir(), name)
	}
	if err != nil {
		if errors.Is(err, state.ErrNameReserved) {
			return c.fail(output.CodeExists, fmt.Sprintf("terminal %q is being created", name), "pairmux ls")
		}
		return c.fail(output.CodeInternal, "reserve terminal name: "+err.Error(), "")
	}
	defer releaseName()

	pane, hasPane, err := c.findPane(name)
	if err != nil {
		return c.muxErr(err)
	}
	dir := c.terminalDir(name)
	if hasPane && !pane.Dead {
		return c.fail(output.CodeExists, fmt.Sprintf("terminal %q already exists", name), "pairmux ls")
	}
	if hasPane && pane.Dead {
		_ = c.Mux.KillWindowOf(pane.PaneID) // reclaim the dead window before reusing the name
	}
	if isDir(dir) {
		// A canonical endpoint namespace should never contain foreign metadata.
		// Preserve it for inspection rather than archiving possible corruption.
		if meta, err := (&journal.Journal{Dir: dir}).ReadMeta(); err == nil && !state.MetaMatchesSocket(meta, c.Mux.SocketName()) {
			return c.fail(output.CodeExists,
				fmt.Sprintf("terminal state %q belongs to socket %q", name, meta.Socket),
				"inspect or move the foreign state directory: "+dir)
		}
		// Reuse of a dead terminal's name: archive its journal, replacing any
		// older archive, then start fresh.
		prev := dir + ".prev"
		_ = os.RemoveAll(prev)
		// A create killed before its pane started leaves the environment
		// hand-off (secrets included) behind; never archive it.
		removePaneEnv(dir)
		if err := os.Rename(dir, prev); err != nil {
			return c.fail(output.CodeInternal, "archive previous terminal dir: "+err.Error(), "")
		}
	}

	shell := shellName()
	var cmdVec []string
	metaShell := shell
	if cmdOverride != "" {
		cmdVec = []string{cmdOverride}
		metaShell = "" // Meta.Shell=="" marks a --cmd program terminal (run refuses it)
	}

	if err := c.Mux.EnsureSession(); err != nil {
		return c.muxErr(err)
	}
	argv, prepEnv, mode, err := shellhooks.Prepare(c.namespaceDir(), shell, cmdVec)
	if err != nil {
		return c.fail(output.CodeInternal, "prepare shell: "+err.Error(), "")
	}

	// Every terminal advertises its identity and canonical state paths. The
	// latter give skills/checkers a stable locator without reimplementing the
	// endpoint hash. Prepare's own env wins on conflict (ruling: its entries
	// take precedence).
	//
	// The endpoint a pane is given is its OWN, not the one that created it.
	// Anything it runs that is itself pairmux — an agent driving further agents
	// — then builds its layer under a socket of its own, which is the whole of
	// what separates one layer from the next (see state.ChildSocket); without
	// it every layer opens windows in the one session and the second `build`
	// terminal anyone creates collides with the first. The state root is pinned
	// beside it so a child resolves the same tree no matter what the tmux server
	// happened to inherit when it started.
	env := map[string]string{
		"PAIRMUX":                 "1",
		"PAIRMUX_NAME":            name,
		"PAIRMUX_STATE_DIR":       c.StateDir,
		"PAIRMUX_STATE_NAMESPACE": c.namespaceDir(),
		"PAIRMUX_TERMINAL_DIR":    dir,
		"PAIRMUX_SOCKET":          state.ChildSocket(c.Mux.SocketName(), name),
		// The scope confines whatever runs here to that child layer and below,
		// and the owner is where report finds this terminal (see hierarchy.go).
		ScopeEnv:       state.ChildSocket(c.Mux.SocketName(), name),
		OwnerSocketEnv: c.Mux.SocketName(),
		// A nested pairmux stays on this backend, whatever auto-selection
		// would pick from inside the pane.
		backend.Env: c.Mux.Backend(),
	}
	for k, v := range prepEnv {
		env[k] = v
	}

	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}

	// Pre-create the journal dir and a 0600 raw.log BEFORE opening the window so
	// pipe-pane can attach with minimal delay (reduces the window in which the
	// shell's first prompt could be emitted before capture starts) and so the
	// pipe-pane `cat >>` appends to a tight-permission file (security ruling).
	j, err := prepareTerminalJournal(dir)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}

	// A program terminal's command is held until the journal is capturing; see
	// startGate. A shell needs no gate — it prints nothing before its rc files
	// run, and hooks mode nudges a prompt cycle to cover the same gap. The
	// built-in backend opens the journal before it starts the program, so it
	// needs neither the gate nor a separate pipe.
	atomic := c.Mux.AtomicJournal()
	var gate *startGate
	if len(argv) > 0 && !atomic {
		if gate, err = newStartGate(j.Dir); err != nil {
			return c.fail(output.CodeInternal, err.Error(), "")
		}
	}
	req := mux.NewWindowReq{Name: name, Dir: cwd, Env: env, Argv: argv, Gate: gate.Path()}
	if atomic {
		req.Journal = j.RawPath()
	} else {
		// The pane's environment (this command's, secrets included) is handed
		// over in the terminal's own 0700 directory; the pane deletes it on
		// start. See mux.NewWindowReq.EnvFile.
		req.EnvFile = filepath.Join(j.Dir, paneEnvFile)
	}
	abandonEnv := func() {
		if req.EnvFile != "" {
			_ = os.Remove(req.EnvFile)
		}
	}
	paneID, err := c.Mux.NewWindow(req)
	if err != nil {
		gate.abandon()
		abandonEnv()
		return c.muxErr(err)
	}
	keepPane := false
	defer func() {
		if !keepPane {
			gate.abandon()
			abandonEnv()
			_ = c.Mux.KillWindowOf(paneID)
		}
	}()
	if !atomic {
		if err := c.Mux.PipePaneAppend(paneID, j.RawPath()); err != nil {
			return c.muxErr(err)
		}
	}
	// Everything the program prints from here on is journalled.
	if err := gate.release(); err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	if err := c.Mux.SetPaneOption(paneID, core.PaneOptName, name); err != nil {
		return c.muxErr(err)
	}

	// The pane's terminal is asked for once, here: prompt classification reads
	// its line discipline on every poll, and a tmux round-trip per poll per agent
	// is exactly the cost this avoids. A failure is not fatal — classification
	// falls back to reading the wording of the prompt, as it always did.
	paneTTY, _ := c.Mux.PaneTTY(paneID)
	meta := core.Meta{Name: name, PaneID: paneID, Tty: paneTTY, Shell: metaShell, Mode: mode, Socket: c.Mux.SocketName(), CreatedAt: time.Now().UTC()}
	if err := j.WriteMeta(meta); err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	if err := j.AppendEvent(core.Event{Type: core.EvCreated, Text: name}); err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}

	// In hooks mode on tmux, nudge one prompt cycle so an OSC-133 A mark is
	// guaranteed to land AFTER pipe-pane is capturing, even if the very first
	// prompt raced ahead of capture. It precedes any run's send offset because
	// `new` waits for the cycle to complete (detect.WaitPromptCycle below). A
	// backend that journals from the first byte has no such race, so it gets no
	// nudge — and none of the stale marks a nudge can leave behind.
	nudged := mode == core.ModeHooks && !atomic
	if nudged {
		_ = c.Mux.SendKeys(paneID, "Enter")
	}

	var next []string
	effMode := mode
	gotHooks, err := detect.WaitReady(j, mode, shellhooks.ReadyTimeout(shell))
	if err != nil {
		return c.fail(output.CodeInternal, "wait ready: "+err.Error(), "")
	}
	if nudged && gotHooks {
		// Not fatal when it never completes: the terminal still works, and the
		// first run merely risks the stale mark this wait exists to absorb.
		_, _ = detect.WaitPromptCycle(j, 5*time.Second)
	}
	if mode == core.ModeHooks && !gotHooks {
		effMode = core.ModeSentinel
		meta.Mode = core.ModeSentinel
		_ = j.WriteMeta(meta)
		next = append(next, "hooks unavailable in this shell config — completion detection degraded to sentinel mode")
	}
	if cmdOverride != "" {
		current, err := state.ResolveAt(c.Mux, c.StateDir, name)
		if err != nil {
			return c.muxErr(err)
		}
		status := deriveTerminalStatus(j, current.Alive, current.Mode, true)
		next = append(next, peekNext(name, status)...)
		keepPane = true
		return c.emit(output.Envelope{Status: string(status), Terminal: name, Mode: string(effMode), Next: next})
	}
	next = append(next, fmt.Sprintf("pairmux run %s \"echo hello\"", name))

	keepPane = true
	return c.emit(output.Envelope{Status: "created", Terminal: name, Mode: string(effMode), Next: next})
}

// paneEnvFile is the name of the environment hand-off inside a terminal's
// directory (mux.NewWindowReq.EnvFile).
const paneEnvFile = ".pane-env"

// removePaneEnv deletes a terminal directory's environment hand-off, if a
// create that died before its pane read it left one behind.
func removePaneEnv(dir string) {
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, paneEnvFile))
	}
}

type closeOnly interface {
	Close() error
}

type terminalJournalOps struct {
	chmod   func(string, os.FileMode) error
	openRaw func(string, int, os.FileMode) (closeOnly, error)
}

func prepareTerminalJournal(dir string) (*journal.Journal, error) {
	return prepareTerminalJournalWith(dir, terminalJournalOps{
		chmod: os.Chmod,
		openRaw: func(path string, flag int, perm os.FileMode) (closeOnly, error) {
			return os.OpenFile(path, flag, perm)
		},
	})
}

// prepareTerminalJournalWith keeps all filesystem preparation ahead of
// NewWindow. The injected operations make each failure path testable without
// exposing or replacing tmux's private command runner.
func prepareTerminalJournalWith(dir string, ops terminalJournalOps) (*journal.Journal, error) {
	j, err := journal.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := ops.chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare terminal directory permissions: %w", err)
	}
	f, err := ops.openRaw(j.RawPath(), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("prepare raw journal: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close raw journal: %w", err)
	}
	return j, nil
}

// cmdRun sends a command, waits for its completion mark or a quiet interactive
// prompt, and returns the shaped output. On timeout it returns status "running"
// and leaves the command for lazy settlement by a later run.
func (c *Ctx) cmdRun(args []string) int {
	const usageLine = `pairmux run <name> "<cmd>" [--timeout 60s] [--head 50] [--tail 200]`
	timeoutS, headS, tailS, pos, err := parseRun(args)
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
	// The command is a single argument, matching the MCP tool contract. Joining
	// variadic tokens on spaces would silently discard quoting the caller's
	// shell already consumed (`run t git commit -m "two words"` would commit
	// "two"), so more than one command token is a loud error instead.
	if len(pos) > 2 {
		return c.fail(output.CodeBadArgs, "run takes the command as one argument",
			fmt.Sprintf("quote the whole command: pairmux run %s %s", name, quotedCommandHint(pos[1:])))
	}
	cmd := ""
	if len(pos) == 2 {
		cmd = pos[1]
	}
	if strings.TrimSpace(cmd) == "" {
		return c.fail(output.CodeBadArgs, "run needs a command", fmt.Sprintf("pairmux run %s \"echo hello\"", name))
	}
	if strings.ContainsAny(cmd, "\n\r") {
		return c.fail(output.CodeBadArgs, "command contains a newline", "use pairmux send for interactive input")
	}

	timeout := 60 * time.Second
	if timeoutS != "" {
		d, err := time.ParseDuration(timeoutS)
		if err != nil {
			return c.usage(usageLine, "bad --timeout: "+err.Error())
		}
		timeout = d
	}
	head, tail := 50, 200
	if headS != "" {
		if v, err := strconv.Atoi(headS); err == nil {
			head = v
		} else {
			return c.usage(usageLine, "bad --head: "+headS)
		}
	}
	if tailS != "" {
		if v, err := strconv.Atoi(tailS); err == nil {
			tail = v
		} else {
			return c.usage(usageLine, "bad --tail: "+tailS)
		}
	}

	term, err := state.ResolveAt(c.Mux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.muxErr(err)
	}
	if !term.Alive {
		return c.fail(output.CodeDead, fmt.Sprintf("terminal %q is dead", name), "pairmux new")
	}
	if term.Meta.Shell == "" {
		return c.fail(output.CodeBadArgs, "this terminal runs a program, not a shell",
			"this terminal runs a program, not a shell — use pairmux send / peek")
	}

	j, err := journal.Open(term.Dir)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	mode := term.Mode

	release, err := j.AcquireWriteLock()
	if err != nil {
		if errors.Is(err, journal.ErrLocked) {
			return c.fail(output.CodeBusy, "another writer holds the lock: "+err.Error(), fmt.Sprintf("pairmux peek %s", name))
		}
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	defer release()

	// Snapshot unseen notes BEFORE the settlement scan below writes anything:
	// settlement appends an EvCmdEnd stamped now, which would otherwise eclipse
	// a note that arrived while the previous command was still unsettled (e.g.
	// the human answered a prompt in the pane, then left a note). Notes
	// appended after this point are merged in at envelope time.
	baseEvents, notes := notesBaseline(j)

	// Lazy settlement: a prior run that timed out left its cmd_start unmatched.
	// Settle it if it has since finished; refuse if it is genuinely still running.
	bounded := shellhooks.SentinelHasBoundary(term.Meta.Shell)
	if pending, ok, perr := j.PendingCmd(); perr == nil && ok {
		res, _ := waitDone(j, pending.Offset, mode, bounded, 0, term.Meta.Tty)
		if res.Outcome != detect.OutcomeDone {
			return c.fail(output.CodeBusy, "a command is still running", fmt.Sprintf("pairmux peek %s", name))
		}
		ec := res.ExitCode
		_ = j.AppendEvent(core.Event{Type: core.EvCmdEnd, CmdID: pending.CmdID, ExitCode: &ec, Offset: res.MarkStart})
	}

	cmdID, err := j.NextCmdID()
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	sendOffset := j.Size()

	// In sentinel mode the shell substitutes its last-status variable into an
	// OSC 7779 marker so completion is detectable without hooks (cmd.exe also
	// gets a prefix that clears that variable first). Drop the exact sent bytes
	// (prefix and suffix included) when shaping so the echoed line disappears.
	sent := cmd
	if mode == core.ModeSentinel {
		sent = shellhooks.SentinelPrefix(term.Meta.Shell) + cmd + shellhooks.SentinelSuffix(term.Meta.Shell)
	}
	if err := recordAndSendCommand(j, core.Event{
		Type: core.EvCmdStart, CmdID: cmdID, Offset: sendOffset, Text: cmd,
	}, func() error {
		return c.Mux.SendLiteral(term.PaneID, sent)
	}, func() error {
		return c.Mux.SendKeys(term.PaneID, "Enter")
	}); err != nil {
		var sendErr *runSendError
		if !errors.As(err, &sendErr) || sendErr.Stage == runSendRecord || sendErr.AbortErr != nil {
			return c.fail(output.CodeInternal, err.Error(), "inspect the terminal journal before retrying")
		}
		if sendErr.Stage == runSendEnter {
			return c.fail(output.CodeTmux, sendErr.Err.Error(), runSendEnterHint(name))
		}
		return c.muxErr(sendErr.Err)
	}

	start := time.Now()
	res, err := waitDone(j, sendOffset, mode, bounded, timeout, term.Meta.Tty)
	if err != nil {
		return c.fail(output.CodeInternal, "wait completion: "+err.Error(), "")
	}

	fullHint := fmt.Sprintf("pairmux log %s --cmd %d", name, cmdID)
	if res.Outcome != detect.OutcomeDone {
		raw, _ := j.ReadRange(sendOffset, res.EndOffset)
		body, omitted := lastLines(cleanNoTrunc(raw, sent), 20)
		env := output.Envelope{
			Status: "running", Terminal: name, Mode: string(mode), Output: body,
			Notes: append(notes, notesArrivedSince(j, baseEvents)...),
			Next:  []string{fmt.Sprintf("pairmux peek %s", name), fullHint},
		}
		if res.Outcome == detect.OutcomeAwaitingInput {
			env.Status = string(core.StatusAwaitingInput)
			env.Next = awaitingNext(name, res.Prompt)
		}
		if omitted > 0 {
			env.Truncated = &output.TruncInfo{OmittedLines: omitted, GetFull: fullHint}
		}
		env.Next = appendGuard(env.Next, j, name)
		return c.emit(env)
	}

	dur := time.Since(start).Milliseconds()
	ec := res.ExitCode
	// Merge in notes appended while the command ran, BEFORE recording its end:
	// they must surface in this envelope (and only this one).
	envNotes := append(notes, notesArrivedSince(j, baseEvents)...)
	_ = j.AppendEvent(core.Event{Type: core.EvCmdEnd, CmdID: cmdID, ExitCode: &ec, Offset: res.MarkStart})
	raw, _ := j.ReadRange(sendOffset, res.MarkStart)
	text, omitted := shape.Render(raw, sent, head, tail)
	env := output.Envelope{
		Status: "done", Terminal: name, Mode: string(mode),
		ExitCode: &ec, DurationMS: &dur, Output: text, Notes: envNotes,
	}
	if omitted > 0 {
		env.Truncated = &output.TruncInfo{OmittedLines: omitted, GetFull: fullHint}
	}
	env.Next = appendGuard(env.Next, j, name)
	return c.emit(env)
}

type runSendStage string

const (
	runSendRecord  runSendStage = "record"
	runSendLiteral runSendStage = "literal"
	runSendEnter   runSendStage = "enter"
)

type runSendError struct {
	Stage    runSendStage
	Err      error
	AbortErr error
}

func (e *runSendError) Error() string {
	if e.AbortErr != nil {
		return fmt.Sprintf("%v; record aborted command: %v", e.Err, e.AbortErr)
	}
	return e.Err.Error()
}

func (e *runSendError) Unwrap() error { return e.Err }

func runSendEnterHint(name string) string {
	return fmt.Sprintf("command text may be buffered; clear it with pairmux send %s --key C-c, then retry", name)
}

// recordAndSendCommand owns the cmd_start/send transition. A tmux failure is
// closed with cmd_end(-1), so PendingCmd never mistakes a command that was not
// entered for a still-running command on the next invocation.
func recordAndSendCommand(j *journal.Journal, start core.Event, sendLiteral, sendEnter func() error) error {
	if err := j.AppendEvent(start); err != nil {
		return &runSendError{Stage: runSendRecord, Err: err}
	}
	abort := func(stage runSendStage, cause error) error {
		exitCode := -1
		abortErr := j.AppendEvent(core.Event{
			Type: core.EvCmdEnd, CmdID: start.CmdID, Offset: j.Size(), ExitCode: &exitCode,
			Text: "command dispatch aborted during " + string(stage),
		})
		return &runSendError{Stage: stage, Err: cause, AbortErr: abortErr}
	}
	if err := sendLiteral(); err != nil {
		return abort(runSendLiteral, err)
	}
	if err := sendEnter(); err != nil {
		return abort(runSendEnter, err)
	}
	return nil
}

// shellWordSafe matches tokens that survive shell re-parsing without quotes.
var shellWordSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// quoteToken renders one token so a POSIX shell parses it back as the same
// single word: unchanged when safe, else single-quoted with '\” escapes.
func quoteToken(tok string) string {
	if tok != "" && shellWordSafe.MatchString(tok) {
		return tok
	}
	return "'" + strings.ReplaceAll(tok, "'", `'\''`) + "'"
}

// quotedCommandHint reconstructs the single-argument form of a variadic run
// command for its rejection hint. Inner quoting restores the word boundaries
// the caller's shell already consumed; the outer quotes make the whole command
// one argument again.
func quotedCommandHint(tokens []string) string {
	inner := make([]string, len(tokens))
	for i, t := range tokens {
		inner[i] = quoteToken(t)
	}
	joined := strings.Join(inner, " ")
	if !strings.ContainsAny(joined, "\"\\$`") {
		return `"` + joined + `"`
	}
	return quoteToken(joined) // single-quote form is safe for any content
}

// waitDone waits for a command's completion mark. Hooks mode uses the
// C-correlated variant (review item R1): the completion D must follow our
// command's own C mark, so a human running commands in the same pane cannot
// spoof completion. bash 3.2 emits no C, which the correlated wait covers by
// accepting a lone D after a short grace. Sentinel mode takes the sentinel, or
// with bounded (shellhooks.SentinelHasBoundary) the boundary printed after it.
// A zero timeout degenerates to a single read-only scan (used for lazy
// settlement).
func waitDone(j *journal.Journal, from int64, mode core.Mode, bounded bool, timeout time.Duration, tty string) (detect.RunResult, error) {
	if mode == core.ModeHooks {
		return detect.WaitCommandCorrelated(j, from, timeout, 0, true, tty)
	}
	return detect.WaitCommand(j, from, mode, bounded, timeout, 0, tty)
}

// deriveTerminalStatus adds terminal-kind context to the journal-derived
// shell status. A --cmd terminal has no pairmux command events or shell prompt
// to make it idle: while its pane exists, the launched program is running.
func deriveTerminalStatus(j *journal.Journal, alive bool, mode core.Mode, program bool) core.Status {
	if alive && program {
		return core.StatusRunning
	}
	return detect.DeriveStatus(j, alive, mode)
}

// cmdPeek shows recent output and the derived status without taking the write
// lock or recording any event (read-only).
func (c *Ctx) cmdPeek(args []string) int {
	var screen bool
	var tailS string
	pos, err := parseFlags(args, flagSpec{
		bools: map[string]*bool{"screen": &screen},
		vals:  map[string]*string{"tail": &tailS},
	})
	if err != nil {
		return c.usage("pairmux peek <name> [--screen | --tail N]", err.Error())
	}
	if len(pos) == 0 {
		return c.usage("pairmux peek <name>", "pairmux ls")
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	name := pos[0]
	if rc, rejected := c.resolveTarget(&name); rejected {
		return rc
	}
	tailN := 60
	if tailS != "" {
		if v, err := strconv.Atoi(tailS); err == nil {
			tailN = v
		} else {
			return c.usage("pairmux peek <name> --tail N", "bad --tail: "+tailS)
		}
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
	status := deriveTerminalStatus(j, term.Alive, term.Mode, term.Meta.Shell == "")
	status, prompt := detect.Classify(j, status, term.Mode, term.Meta.Tty)

	var body string
	var trunc *output.TruncInfo
	if screen && term.Alive {
		out, err := c.Mux.CapturePane(term.PaneID, 0)
		if err != nil {
			return c.muxErr(err)
		}
		body = trimTrailingBlank(shape.StripBoundary(out))
	} else {
		raw, start, err := j.TailBytes(64 * 1024)
		if err != nil {
			return c.fail(output.CodeInternal, err.Error(), "")
		}
		var omitted int
		body, omitted = lastLines(cleanNoTrunc(raw, ""), tailN)
		if omitted > 0 || start > 0 {
			trunc = &output.TruncInfo{
				OmittedLines: omitted,
				OmittedBytes: start,
				GetFull:      fmt.Sprintf("pairmux log %s --range 1:end", name),
			}
		}
	}

	next := peekNext(name, status)
	if status == core.StatusAwaitingInput {
		next = awaitingNext(name, prompt)
	}
	return c.emit(output.Envelope{
		Status: string(status), Terminal: name, Mode: string(term.Mode),
		Output: body, Truncated: trunc, Notes: readUnseenNotes(j),
		Next: appendGuard(next, j, name),
	})
}

func peekNext(name string, status core.Status) []string {
	switch status {
	case core.StatusRunning:
		return []string{fmt.Sprintf("pairmux peek %s", name), fmt.Sprintf("pairmux send %s --text ... --enter", name)}
	case core.StatusDead:
		return []string{fmt.Sprintf("pairmux log %s", name), "pairmux new"}
	default:
		return []string{fmt.Sprintf("pairmux run %s \"echo hello\"", name)}
	}
}

// awaitingNext returns the next steps for an awaiting-input terminal.
//
// A secret prompt must never tempt the agent into typing or guessing a
// credential, so the only path offered is human handoff. That verdict now comes
// from the classification rather than from matching words, which is what makes
// it hold for a locale or a tool the patterns have never seen.
//
// An inferred prompt gets a third answer: pairmux believes the terminal is
// waiting but cannot say what for, so it asks the agent to look before it
// types. Offering `send` on that evidence would invite an answer into a command
// that was merely thinking.
func awaitingNext(name string, p detect.Prompt) []string {
	switch p.Kind {
	case detect.KindSecret:
		return []string{
			"do NOT guess or type secrets",
			fmt.Sprintf("pairmux wait %s --human --notify   # hand off to the human", name),
		}
	case detect.KindInferred:
		if p.Raw {
			// A program holds the keyboard: a pager, an editor, another agent's
			// terminal UI. Two things that are true of the line-oriented case are
			// false here. Its screen is a rendering, not a stream, so only --screen
			// shows what is actually on it. And there is no command to complete, so
			// --done would block until the *program itself exits* — for a terminal
			// UI that is never, which turns the one hint an unsure agent is most
			// likely to follow into a hang. Wait for something nameable instead.
			return []string{
				"a full-screen program holds this terminal — look at the screen, not the stream",
				fmt.Sprintf("pairmux peek %s --screen", name),
				fmt.Sprintf("pairmux wait %s --pattern RE   # if it turns out to still be working", name),
				fmt.Sprintf("pairmux send %s --text <answer> --enter", name),
			}
		}
		// Inference, not recognition: nothing was killed and the command may
		// simply be thinking, so look first, and carry on waiting if it is.
		return []string{
			"quiet mid-line, but no prompt was recognised — look before you answer",
			fmt.Sprintf("pairmux peek %s --screen", name),
			fmt.Sprintf("pairmux wait %s --done   # if it turns out to still be working", name),
			fmt.Sprintf("pairmux send %s --text <answer> --enter", name),
		}
	default:
		return []string{fmt.Sprintf("pairmux send %s --text <answer> --enter", name)}
	}
}

// cmdSend delivers input to a terminal (text, then keys, then Enter) without
// taking the write lock, so interactive answers can reach a running program.
func (c *Ctx) cmdSend(args []string) int {
	var text string
	var enter bool
	var keys []string
	seen := map[string]bool{}
	pos, err := parseFlags(args, flagSpec{
		bools: map[string]*bool{"enter": &enter},
		vals:  map[string]*string{"text": &text},
		lists: map[string]*[]string{"key": &keys},
		seen:  seen,
	})
	if err != nil {
		return c.usage("pairmux send <name> [--text S] [--key K ...] [--enter]", err.Error())
	}
	if len(pos) == 0 {
		return c.usage("pairmux send <name> [--text S] [--key K ...] [--enter]", "pairmux ls")
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	name := pos[0]
	if rc, rejected := c.resolveTarget(&name); rejected {
		return rc
	}
	if !seen["text"] && !seen["key"] && !seen["enter"] {
		return c.fail(output.CodeBadArgs, "send needs at least one of --text/--key/--enter",
			fmt.Sprintf("pairmux send %s --text hello --enter", name))
	}
	for _, k := range keys {
		if !validKey(k) {
			return c.fail(output.CodeBadArgs, fmt.Sprintf("invalid key %q", k), keyHint(k))
		}
	}

	term, err := state.ResolveAt(c.Mux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.muxErr(err)
	}
	if !term.Alive {
		return c.fail(output.CodeDead, fmt.Sprintf("terminal %q is dead", name), "pairmux new")
	}
	j, err := journal.Open(term.Dir)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	running := deriveTerminalStatus(j, term.Alive, term.Mode, term.Meta.Shell == "") == core.StatusRunning

	// Serialize against other senders only (see AcquireSendLock): a send must
	// still get through a held write lock, or a handoff prompt could never be
	// answered, but two senders interleaving keystrokes produce a line that is
	// neither of theirs.
	releaseSend, err := j.AcquireSendLock()
	if err != nil {
		return c.fail(output.CodeBusy, "another send is in flight: "+err.Error(),
			fmt.Sprintf("retry, or pairmux peek %s to see what landed", name))
	}
	defer releaseSend()

	if seen["text"] {
		if err := c.Mux.SendLiteral(term.PaneID, text); err != nil {
			return c.muxErr(err)
		}
	}
	if len(keys) > 0 {
		if err := c.Mux.SendKeys(term.PaneID, keys...); err != nil {
			return c.muxErr(err)
		}
	}
	if enter {
		if err := c.Mux.SendKeys(term.PaneID, "Enter"); err != nil {
			return c.muxErr(err)
		}
	}

	// Record that the agent acted, without recording what it typed. This is what
	// closes a note: a terminal driven by send alone — a full-screen program, an
	// agent's own terminal UI — never completes a command, so cmd_end can never
	// arrive to mark the note answered, and every later wait would hand back the
	// same stale note forever. See sentParts for why the content is not stored.
	_ = j.AppendEvent(core.Event{Type: core.EvSent, Text: sentParts(seen["text"], keys, enter)})

	next := []string{fmt.Sprintf("pairmux peek %s", name)}
	if running {
		next = append([]string{"a command is running; sent input goes to it"}, next...)
	}
	// The send lock reaches other pairmux processes, not a person at a keyboard:
	// their keystrokes go straight to the pane. When one is watching this very
	// window, say so — sharing an input line is the one collision that cannot be
	// prevented, only noticed.
	if n, active, err := c.Mux.PaneAudience(term.PaneID); err == nil && n > 0 && active {
		next = append([]string{"a human is attached to this window — you are sharing its input line"}, next...)
	}
	return c.emit(output.Envelope{Status: "sent", Terminal: name, Mode: string(term.Mode), Next: next})
}

// shapedViewCap bounds the implicit journal-tail view. Explicit --grep and
// --range requests scan the whole journal so their results are never silently
// scoped to a suffix.
const shapedViewCap = 4 << 20

// cmdLs lists every terminal with its derived status. --tree descends into
// the layers terminals are driving, so a coordinator sees its whole fleet —
// workers, their sub-agents, and theirs — in one call, each row addressable by
// the path it shows.
func (c *Ctx) cmdLs(args []string) int {
	var tree, plain bool
	if _, err := parseFlags(args, flagSpec{bools: map[string]*bool{"tree": &tree, "r": &tree, "plain": &plain}}); err != nil {
		return c.usage("pairmux ls [--tree] [--plain]", err.Error())
	}
	// A person typing a bare ls at their own terminal wants to manage the
	// sessions, not read a table: hand them the session manager. Any flag,
	// --json, a pipe, or a caller inside a terminal or an agent's tool shell
	// keeps the listing (see humanAtTerminal).
	if len(args) == 0 && c.humanAtTerminal() {
		if rc, rejected := c.rejectInvalidSocket(); rejected {
			return rc
		}
		if rc, err := c.runTUI(time.Second); err == nil {
			return rc
		}
		// A console that cannot host the TUI gets the listing instead.
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	nodes, err := c.walkTree(tree)
	if err != nil {
		return c.muxErr(err)
	}
	if len(nodes) == 0 {
		return c.emit(output.Envelope{Status: "ok", Next: []string{"pairmux new"}})
	}
	rows := make([]output.TerminalRow, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, n.row(tree))
	}
	var next []string
	if !tree {
		for _, r := range rows {
			if r.ChildSocket != "" {
				next = append(next, "pairmux ls --tree   # include the terminals "+r.Name+" is driving (address them as "+r.Name+"/<name>)")
				break
			}
		}
	}
	return c.emit(output.Envelope{Status: "ok", Terminals: rows, Next: next})
}

// lockHolderPID reports the pid actually holding a terminal's write lock, 0
// when it is free. journal.LockHolder alone is not enough: the recorded pid
// stays in write.lock after release, so heldness is probed with a non-blocking
// shared lock on a read-only descriptor (nothing is written; an instant
// success is released immediately).
func lockHolderPID(dir string) int {
	f, err := os.Open(filepath.Join(dir, "write.lock"))
	if err != nil {
		return 0 // never locked (or unreadable): free as far as we can tell
	}
	defer f.Close()
	if err := flock.TryLock(f, false); err == nil {
		_ = flock.Unlock(f)
		return 0
	}
	if pid, ok := journal.LockHolder(dir); ok {
		return pid
	}
	return -1 // held, but the holder pid was unreadable
}

// cmdKill kills a terminal's window, or with --all every managed window.
// Journals are retained on disk either way.
func (c *Ctx) cmdKill(args []string) int {
	var all bool
	pos, err := parseFlags(args, flagSpec{bools: map[string]*bool{"all": &all}})
	if err != nil {
		return c.usage("pairmux kill <name> | --all", err.Error())
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	if all {
		if len(pos) > 0 {
			return c.usage("pairmux kill --all", "either --all or a name, not both")
		}
		return c.killAll()
	}
	if len(pos) == 0 {
		return c.usage("pairmux kill <name> | --all", "pairmux ls")
	}
	name := pos[0]
	if rc, rejected := c.resolveTarget(&name); rejected {
		return rc
	}
	term, err := state.ResolveAt(c.Mux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.muxErr(err)
	}
	// Descend before killing the pane: once the window is gone the terminal is
	// unresolvable, and its layer would be orphaned with nothing left pointing
	// at it.
	layers := c.killLayers(c.Mux.SocketName(), name, 0)
	if term.Alive && term.PaneID != "" {
		_ = c.Mux.KillWindowOf(term.PaneID) // ignore: pane may already be gone
	}
	removePaneEnv(term.Dir)
	if j, err := journal.Open(term.Dir); err == nil {
		_ = j.AppendEvent(core.Event{Type: core.EvNote, Text: "killed"})
	}
	next := []string{
		"journal retained at " + term.Dir,
		fmt.Sprintf("pairmux prune %s reclaims its disk when the history is no longer needed", name),
		"pairmux ls",
	}
	if line := layersNext(layers); line != "" {
		next = append([]string{line}, next...)
	}
	return c.emit(output.Envelope{Status: "killed", Terminal: name, Next: next})
}

// killAll kills every managed window on the socket and lists the names killed.
func (c *Ctx) killAll() int {
	panes, err := c.Mux.ListManaged()
	if err != nil {
		if !state.IsNoServer(err) {
			return c.muxErr(err)
		}
		panes = nil
	}
	var names []string
	var layers []string
	for _, p := range panes {
		if state.ValidName(p.Name) {
			layers = append(layers, c.killLayers(c.Mux.SocketName(), p.Name, 0)...)
		}
		_ = c.Mux.KillWindowOf(p.PaneID) // ignore: pane may already be gone
		if state.ValidName(p.Name) {
			dir := c.terminalDir(p.Name)
			if term, err := state.ResolveAt(c.Mux, c.StateDir, p.Name); err == nil {
				dir = term.Dir
			}
			if j, err := journal.Open(dir); err == nil {
				_ = j.AppendEvent(core.Event{Type: core.EvNote, Text: "killed"})
			}
		}
		names = append(names, p.Name)
	}
	sort.Strings(names)
	next := []string{
		"journals retained under " + c.namespaceDir(),
		"pairmux prune reclaims dead-terminal disk when the history is no longer needed",
		"pairmux ls",
	}
	if line := layersNext(layers); line != "" {
		next = append([]string{line}, next...)
	}
	return c.emit(output.Envelope{Status: "killed", Output: strings.Join(names, "\n"), Next: next})
}

// sentParts names which halves of a send were delivered, for the EvSent event.
// It deliberately describes the shape of the input and never its content: a
// human answering a handoff types the credential through send, and the event
// log is a file that outlives the screen. Pure: unit-tested directly.
func sentParts(text bool, keys []string, enter bool) string {
	var parts []string
	if text {
		parts = append(parts, "text")
	}
	if len(keys) > 0 {
		parts = append(parts, "keys")
	}
	if enter {
		parts = append(parts, "enter")
	}
	return strings.Join(parts, "+")
}

// journalSizeGuard is review item R3: past this raw.log size, run, peek and
// wait warn the agent that the journal needs rotating via kill + new.
const journalSizeGuard = 256 << 20

// appendGuard appends the R3 large-journal warning to next when raw.log has
// outgrown the guard threshold. The rotation it suggests archives the old
// journal as "<name>.prev"; prune is what actually returns the disk.
func appendGuard(next []string, j *journal.Journal, name string) []string {
	size := j.Size()
	if size <= journalSizeGuard {
		return next
	}
	return append(next, fmt.Sprintf(
		"journal is large (%dMB); rotate with pairmux kill %s + pairmux new, then pairmux prune %s to reclaim disk",
		size>>20, name, name))
}

// cmdVersion prints the build version.
func (c *Ctx) cmdVersion() int {
	if c.JSON {
		return c.emit(output.Envelope{Status: "ok", Output: version.Version})
	}
	fmt.Fprintln(c.Stdout, version.Version)
	return 0
}

// --- small helpers ---

// existingNamesScoped is the socket-aware variant used while allocating an
// automatic name. The older Ctx helper remains for generic error hints.
func (c *Ctx) existingNamesScoped() []string {
	terms, err := state.ListAt(c.Mux, c.StateDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(terms))
	for _, term := range terms {
		names = append(names, term.Name)
	}
	return names
}

// findPane returns the managed pane for name, tolerating tmux's no-server state.
func (c *Ctx) findPane(name string) (mux.PaneInfo, bool, error) {
	panes, err := c.Mux.ListManaged()
	if err != nil {
		if state.IsNoServer(err) {
			return mux.PaneInfo{}, false, nil
		}
		return mux.PaneInfo{}, false, err
	}
	for _, p := range panes {
		if p.Name == name {
			return p, true, nil
		}
	}
	return mux.PaneInfo{}, false, nil
}

// scanCompletion does one read-only scan for the completion mark of a command
// sent at offset from, returning the mark's start offset if present. bounded is
// as for waitDone.
func scanCompletion(j *journal.Journal, from int64, mode core.Mode, bounded bool) (start int64, ok bool) {
	res, err := detect.WaitCompletion(j, from, mode, bounded, 0, 0)
	if err != nil || res.Outcome != detect.OutcomeDone {
		return 0, false
	}
	return res.MarkStart, true
}

// shellName is the shell a new terminal runs: $PAIRMUX_SHELL when set, else
// $SHELL, defaulting to bash when unset. Preserve the configured path so shells
// with native integration (notably fish) launch the same binary.
//
// On Windows $SHELL is honoured only when it names a real file — Git Bash
// exports a POSIX path such as /usr/bin/bash that Windows cannot start — and
// the default is PowerShell 7 (pwsh), then Windows PowerShell, then cmd.exe.
func shellName() string {
	if sh := os.Getenv("PAIRMUX_SHELL"); sh != "" {
		return sh
	}
	if runtime.GOOS == "windows" {
		if sh := os.Getenv("SHELL"); sh != "" {
			if fi, err := os.Stat(sh); err == nil && !fi.IsDir() {
				return sh
			}
		}
		for _, name := range []string{"pwsh.exe", "powershell.exe"} {
			if p, err := exec.LookPath(name); err == nil {
				return p
			}
		}
		if c := os.Getenv("COMSPEC"); c != "" {
			return c
		}
		return "cmd.exe"
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		return "bash"
	}
	return sh
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

var (
	fKeyRe  = regexp.MustCompile(`^F([1-9]|1[0-2])$`)
	ctrlRe  = regexp.MustCompile(`^C-[a-z]$`)
	metaKey = regexp.MustCompile(`^M-[a-z]$`)
)

// validKey reports whether k is in the send allowlist. Allowed names are already
// valid tmux key names, so they pass straight through to send-keys.
func validKey(k string) bool {
	switch k {
	case "Enter", "Escape", "Tab", "Space", "Up", "Down", "Left", "Right",
		"Home", "End", "PPage", "NPage", "BSpace", "DC":
		return true
	}
	return fKeyRe.MatchString(k) || ctrlRe.MatchString(k) || metaKey.MatchString(k)
}

// keyHint builds the E_BAD_ARGS hint for an invalid --key. The commonest
// mistake is passing a plain character (--key q) where --text belongs; lead
// with that fix before listing the valid key names.
func keyHint(k string) string {
	const valid = "valid keys: Enter Escape Tab Space Up Down Left Right Home End PPage NPage BSpace DC F1-F12 C-a..C-z M-a..M-z"
	if r := []rune(k); len(r) == 1 && unicode.IsPrint(r[0]) && r[0] != ' ' {
		return fmt.Sprintf("use --text %s to type the character %s (then --enter if needed); %s", k, k, valid)
	}
	return valid
}
