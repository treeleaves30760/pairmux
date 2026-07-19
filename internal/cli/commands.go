package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/detect"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/shape"
	"github.com/treeleaves30760/pairmux/internal/shellhooks"
	"github.com/treeleaves30760/pairmux/internal/state"
	"github.com/treeleaves30760/pairmux/internal/tmux"
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
		if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
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
		return c.tmuxErr(err)
	}
	dir := c.terminalDir(name)
	if hasPane && !pane.Dead {
		return c.fail(output.CodeExists, fmt.Sprintf("terminal %q already exists", name), "pairmux ls")
	}
	if hasPane && pane.Dead {
		_ = c.Tmux.KillWindowOf(pane.PaneID) // reclaim the dead window before reusing the name
	}
	if isDir(dir) {
		// A canonical endpoint namespace should never contain foreign metadata.
		// Preserve it for inspection rather than archiving possible corruption.
		if meta, err := (&journal.Journal{Dir: dir}).ReadMeta(); err == nil && !state.MetaMatchesSocket(meta, c.Tmux.Socket) {
			return c.fail(output.CodeExists,
				fmt.Sprintf("terminal state %q belongs to socket %q", name, meta.Socket),
				"inspect or move the foreign state directory: "+dir)
		}
		// Reuse of a dead terminal's name: archive its journal, replacing any
		// older archive, then start fresh.
		prev := dir + ".prev"
		_ = os.RemoveAll(prev)
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

	if err := c.Tmux.EnsureSession(); err != nil {
		return c.tmuxErr(err)
	}
	argv, prepEnv, mode, err := shellhooks.Prepare(c.namespaceDir(), shell, cmdVec)
	if err != nil {
		return c.fail(output.CodeInternal, "prepare shell: "+err.Error(), "")
	}

	// Every terminal advertises its identity and canonical state paths. The
	// latter give skills/checkers a stable locator without reimplementing the
	// endpoint hash. Prepare's own env wins on conflict (ruling: its entries
	// take precedence).
	env := map[string]string{
		"PAIRMUX":                 "1",
		"PAIRMUX_NAME":            name,
		"PAIRMUX_STATE_NAMESPACE": c.namespaceDir(),
		"PAIRMUX_TERMINAL_DIR":    dir,
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

	paneID, err := c.Tmux.NewWindow(tmux.NewWindowReq{Name: name, Dir: cwd, Env: env, Argv: argv})
	if err != nil {
		return c.tmuxErr(err)
	}
	keepPane := false
	defer func() {
		if !keepPane {
			_ = c.Tmux.KillWindowOf(paneID)
		}
	}()
	if err := c.Tmux.PipePaneAppend(paneID, j.RawPath()); err != nil {
		return c.tmuxErr(err)
	}
	if err := c.Tmux.SetPaneOption(paneID, core.PaneOptName, name); err != nil {
		return c.tmuxErr(err)
	}

	meta := core.Meta{Name: name, PaneID: paneID, Shell: metaShell, Mode: mode, Socket: c.Tmux.Socket, CreatedAt: time.Now().UTC()}
	if err := j.WriteMeta(meta); err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	if err := j.AppendEvent(core.Event{Type: core.EvCreated, Text: name}); err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}

	// In hooks mode, nudge one prompt cycle so an OSC-133 A mark is guaranteed
	// to land AFTER pipe-pane is capturing, even if the very first prompt raced
	// ahead of capture. Harmless: it precedes any run's send offset.
	if mode == core.ModeHooks {
		_ = c.Tmux.SendKeys(paneID, "Enter")
	}

	var next []string
	effMode := mode
	gotHooks, err := detect.WaitReady(j, mode, 5*time.Second)
	if err != nil {
		return c.fail(output.CodeInternal, "wait ready: "+err.Error(), "")
	}
	if mode == core.ModeHooks && !gotHooks {
		effMode = core.ModeSentinel
		meta.Mode = core.ModeSentinel
		_ = j.WriteMeta(meta)
		next = append(next, "hooks unavailable in this shell config — completion detection degraded to sentinel mode")
	}
	if cmdOverride != "" {
		current, err := state.ResolveAt(c.Tmux, c.StateDir, name)
		if err != nil {
			return c.tmuxErr(err)
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
	const usageLine = `pairmux run <name> <cmd...> [--timeout 60s] [--head 50] [--tail 200]`
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
	if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
		return rc
	}
	cmd := strings.Join(pos[1:], " ")
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
	if pending, ok, perr := j.PendingCmd(); perr == nil && ok {
		res, _ := waitDone(j, pending.Offset, mode, 0)
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
	// OSC 7779 marker so completion is detectable without hooks. Drop the exact
	// sent bytes (suffix included) when shaping so the echoed line disappears.
	sent := cmd
	if mode == core.ModeSentinel {
		sent = cmd + shellhooks.SentinelSuffix(term.Meta.Shell)
	}
	if err := recordAndSendCommand(j, core.Event{
		Type: core.EvCmdStart, CmdID: cmdID, Offset: sendOffset, Text: cmd,
	}, func() error {
		return c.Tmux.SendLiteral(term.PaneID, sent)
	}, func() error {
		return c.Tmux.SendKeys(term.PaneID, "Enter")
	}); err != nil {
		var sendErr *runSendError
		if !errors.As(err, &sendErr) || sendErr.Stage == runSendRecord || sendErr.AbortErr != nil {
			return c.fail(output.CodeInternal, err.Error(), "inspect the terminal journal before retrying")
		}
		if sendErr.Stage == runSendEnter {
			return c.fail(output.CodeTmux, sendErr.Err.Error(), runSendEnterHint(name))
		}
		return c.tmuxErr(sendErr.Err)
	}

	start := time.Now()
	res, err := waitDone(j, sendOffset, mode, timeout)
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

// waitDone waits for a command's completion mark. Hooks mode uses the
// C-correlated variant (review item R1): the completion D must follow our
// command's own C mark, so a human running commands in the same pane cannot
// spoof completion. bash 3.2 emits no C, which the correlated wait covers by
// accepting a lone D after a short grace. Sentinel mode is unchanged. A zero
// timeout degenerates to a single read-only scan (used for lazy settlement).
func waitDone(j *journal.Journal, from int64, mode core.Mode, timeout time.Duration) (detect.RunResult, error) {
	if mode == core.ModeHooks {
		return detect.WaitCommandCorrelated(j, from, timeout, 0, true)
	}
	return detect.WaitCommand(j, from, mode, timeout, 0)
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
	if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
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

	term, err := state.ResolveAt(c.Tmux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.tmuxErr(err)
	}
	j, err := journal.Open(term.Dir)
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}
	status := deriveTerminalStatus(j, term.Alive, term.Mode, term.Meta.Shell == "")
	status, prompt := detect.Refine(j, status, term.Mode)

	var body string
	var trunc *output.TruncInfo
	if screen && term.Alive {
		out, err := c.Tmux.CapturePane(term.PaneID, 0)
		if err != nil {
			return c.tmuxErr(err)
		}
		body = trimTrailingBlank(out)
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

// awaitingNext returns the next steps for an awaiting-input terminal. A secret
// prompt (password/passphrase/passcode class) must never tempt the agent into
// typing or guessing a credential: the only offered path is human handoff.
func awaitingNext(name, prompt string) []string {
	if detect.LooksSecret(prompt) {
		return []string{
			"do NOT guess or type secrets",
			fmt.Sprintf("pairmux wait %s --human --notify   # hand off to the human", name),
		}
	}
	return []string{fmt.Sprintf("pairmux send %s --text <answer> --enter", name)}
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
	if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
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
	running := deriveTerminalStatus(j, term.Alive, term.Mode, term.Meta.Shell == "") == core.StatusRunning

	if seen["text"] {
		if err := c.Tmux.SendLiteral(term.PaneID, text); err != nil {
			return c.tmuxErr(err)
		}
	}
	if len(keys) > 0 {
		if err := c.Tmux.SendKeys(term.PaneID, keys...); err != nil {
			return c.tmuxErr(err)
		}
	}
	if enter {
		if err := c.Tmux.SendKeys(term.PaneID, "Enter"); err != nil {
			return c.tmuxErr(err)
		}
	}

	next := []string{fmt.Sprintf("pairmux peek %s", name)}
	if running {
		next = append([]string{"a command is running; sent input goes to it"}, next...)
	}
	return c.emit(output.Envelope{Status: "sent", Terminal: name, Mode: string(term.Mode), Next: next})
}

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
	if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
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

	term, err := state.ResolveAt(c.Tmux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.tmuxErr(err)
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
		return c.logCmd(name, j, term.Mode, id)
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

// shapedViewCap bounds the implicit journal-tail view. Explicit --grep and
// --range requests scan the whole journal so their results are never silently
// scoped to a suffix.
const shapedViewCap = 4 << 20

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
func (c *Ctx) logCmd(name string, j *journal.Journal, mode core.Mode, id int) int {
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
		if mk, ok := scanCompletion(j, from, mode); ok {
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

// cmdLs lists every terminal with its derived status.
func (c *Ctx) cmdLs(args []string) int {
	if _, err := parseFlags(args, flagSpec{}); err != nil {
		return c.usage("pairmux ls", err.Error())
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	terms, err := state.ListAt(c.Tmux, c.StateDir)
	if err != nil {
		return c.tmuxErr(err)
	}
	if len(terms) == 0 {
		return c.emit(output.Envelope{Status: "ok", Next: []string{"pairmux new"}})
	}
	rows := make([]output.TerminalRow, 0, len(terms))
	for _, t := range terms {
		j := &journal.Journal{Dir: t.Dir}
		status := deriveTerminalStatus(j, t.Alive, t.Mode, t.Meta.Shell == "")
		status, _ = detect.Refine(j, status, t.Mode)
		row := output.TerminalRow{Name: t.Name, Status: string(status), Mode: string(t.Mode)}
		if status == core.StatusRunning || status == core.StatusAwaitingInput {
			if pending, ok, err := j.PendingCmd(); err == nil && ok {
				row.CurrentCmd = pending.Text
			}
		}
		row.LockHolder = lockHolderPID(t.Dir)
		row.Notes = len(readUnseenNotes(j))
		if mt, ok := j.LastModified(); ok {
			row.LastActivity = mt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	return c.emit(output.Envelope{Status: "ok", Terminals: rows})
}

// lockHolderPID reports the pid actually holding a terminal's write lock, 0
// when it is free. journal.LockHolder alone is not enough: the recorded pid
// stays in write.lock after release, so heldness is probed with a non-blocking
// shared flock on a read-only descriptor (nothing is written; an instant
// success is released immediately).
func lockHolderPID(dir string) int {
	f, err := os.Open(filepath.Join(dir, "write.lock"))
	if err != nil {
		return 0 // never locked (or unreadable): free as far as we can tell
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
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
	if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
		return rc
	}
	term, err := state.ResolveAt(c.Tmux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.tmuxErr(err)
	}
	if term.Alive && term.PaneID != "" {
		_ = c.Tmux.KillWindowOf(term.PaneID) // ignore: pane may already be gone
	}
	if j, err := journal.Open(term.Dir); err == nil {
		_ = j.AppendEvent(core.Event{Type: core.EvNote, Text: "killed"})
	}
	return c.emit(output.Envelope{
		Status: "killed", Terminal: name,
		Next: []string{"journal retained at " + term.Dir, "pairmux ls"},
	})
}

// killAll kills every managed window on the socket and lists the names killed.
func (c *Ctx) killAll() int {
	panes, err := c.Tmux.ListManaged()
	if err != nil {
		if !state.IsNoServer(err) {
			return c.tmuxErr(err)
		}
		panes = nil
	}
	var names []string
	for _, p := range panes {
		_ = c.Tmux.KillWindowOf(p.PaneID) // ignore: pane may already be gone
		if state.ValidName(p.Name) {
			dir := c.terminalDir(p.Name)
			if term, err := state.ResolveAt(c.Tmux, c.StateDir, p.Name); err == nil {
				dir = term.Dir
			}
			if j, err := journal.Open(dir); err == nil {
				_ = j.AppendEvent(core.Event{Type: core.EvNote, Text: "killed"})
			}
		}
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return c.emit(output.Envelope{
		Status: "killed", Output: strings.Join(names, "\n"),
		Next: []string{"journals retained under " + c.namespaceDir(), "pairmux ls"},
	})
}

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
	if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
		return rc
	}
	text := strings.Join(pos[1:], " ")

	term, err := state.ResolveAt(c.Tmux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.tmuxErr(err)
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
		Next: []string{"the agent sees this note on its next run/peek/wait of " + name},
	})
}

// unseenNotes returns the texts of EvNote events recorded after the last
// EvCmdEnd — human messages the agent has not had a chance to see. When no
// command has ever completed, every note is unseen.
func unseenNotes(evs []core.Event) []string {
	var lastEnd time.Time
	for _, ev := range evs {
		if ev.Type == core.EvCmdEnd {
			lastEnd = ev.TS
		}
	}
	var notes []string
	for _, ev := range evs {
		if ev.Type == core.EvNote && (lastEnd.IsZero() || ev.TS.After(lastEnd)) {
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

// journalSizeGuard is review item R3: past this raw.log size, run and peek
// warn the agent that the journal needs rotating via kill + new.
const journalSizeGuard = 256 << 20

// appendGuard appends the R3 large-journal warning to next when raw.log has
// outgrown the guard threshold.
func appendGuard(next []string, j *journal.Journal, name string) []string {
	size := j.Size()
	if size <= journalSizeGuard {
		return next
	}
	return append(next, fmt.Sprintf("journal is large (%dMB); consider pairmux kill %s + pairmux new", size>>20, name))
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
	terms, err := state.ListAt(c.Tmux, c.StateDir)
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
func (c *Ctx) findPane(name string) (tmux.PaneInfo, bool, error) {
	panes, err := c.Tmux.ListManaged()
	if err != nil {
		if state.IsNoServer(err) {
			return tmux.PaneInfo{}, false, nil
		}
		return tmux.PaneInfo{}, false, err
	}
	for _, p := range panes {
		if p.Name == name {
			return p, true, nil
		}
	}
	return tmux.PaneInfo{}, false, nil
}

// scanCompletion does one read-only scan for the completion mark of a command
// sent at offset from, returning the mark's start offset if present.
func scanCompletion(j *journal.Journal, from int64, mode core.Mode) (start int64, ok bool) {
	res, err := detect.WaitCompletion(j, from, mode, 0, 0)
	if err != nil || res.Outcome != detect.OutcomeDone {
		return 0, false
	}
	return res.MarkStart, true
}

// shellName is $SHELL, defaulting to bash when unset. Preserve the configured
// path so shells with native integration (notably fish) launch the same binary.
func shellName() string {
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
