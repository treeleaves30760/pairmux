---
title: CLI Reference
description: Every pairmux command with its flags, representative JSON examples, and the full pairmux.v1 envelope schema.
---

# CLI Reference

```text
usage: pairmux [--json] [--socket S] <command> [args]
```

Ordinary non-interactive CLI commands reply through the **`pairmux.v1` envelope**. Add `--json` for the machine-readable one-line form (shown throughout this page); without it, pairmux prints a friendly text block. `attach` and `watch` are interactive human interfaces, `help` is always plain text, and `mcp serve` reserves stdout for its JSON-RPC stream rather than emitting an envelope. The examples below are representative snapshots: timing, output, and contextual `next` hints vary with terminal state, and an example may omit a contextual hint that is not relevant to the behavior being illustrated.

## Global flags

| flag | meaning |
|------|---------|
| `--json` | Emit a compact JSON envelope instead of the text block. May appear before or after the command. |
| `--socket S` | Use endpoint `S` — the tmux socket, or the built-in server's socket name (isolates a set of terminals). Overrides `$PAIRMUX_SOCKET`. |
| `--` | Ends global-flag parsing, for a command that itself contains `--json`/`--socket`. |

## Environment

| variable | meaning |
|----------|---------|
| `PAIRMUX_SOCKET` | Default endpoint (socket) name; `--socket` overrides it. Socket labels are validated before the backend runs. |
| `PAIRMUX_BACKEND` | Terminal backend: `tmux`, `builtin`, or `auto` (the same as unset). Auto selects `builtin` on Windows; else `builtin` if a built-in server is already running for this socket; else `tmux` if it is on PATH; else `builtin`. Every terminal is created with it set to its own backend, so nested layers stay on it. `tmux` on Windows is an error; an invalid value is `E_BAD_ARGS` (`help`, `version`, `doctor`, `skill`, and `update` still work). See [Concepts](./concepts.md#terminal-backends). |
| `PAIRMUX_TMPDIR` | Root of the built-in server's runtime directory (socket, lock, `<socket>.log`). Unix: `$PAIRMUX_TMPDIR`, else `$TMUX_TMPDIR`, else `/tmp`, then `pairmux-<uid>/`. Windows: `%PAIRMUX_TMPDIR%`, else `%LOCALAPPDATA%\pairmux`, then `run\`. Set it to a short writable directory if the socket path is too long. |
| `PAIRMUX_SHELL` | Interactive shell for `new`, on every platform; overrides `SHELL` and the Windows default. |
| `SHELL` | Interactive shell for `new` when `PAIRMUX_SHELL` is unset. On Windows it is honored only if it names an existing file; the default there is `pwsh.exe`, else `powershell.exe`, else `%COMSPEC%`. |
| `PAIRMUX_STATE_DIR` | Journal root. Otherwise `$XDG_STATE_HOME/pairmux`, then `~/.local/state/pairmux`. |
| `TMUX_TMPDIR` | tmux's socket root, part of a tmux endpoint's identity; on Unix also the built-in runtime root when `PAIRMUX_TMPDIR` is unset. |
| `PAIRMUX_SECRET_PROMPT_RE` | An RE2 pattern that extends (never replaces) secret-prompt recognition. |

Journals live in an isolated hash per endpoint under `<root>/.sockets/`: for tmux, of the canonical `TMUX_TMPDIR`, uid, and socket; built-in endpoints hash a separate identity, so equal terminal names on the two backends never collide, and each backend lists only its own terminals, live or dead. For the conventional default tmux endpoint only, journals from the historical `<root>/<terminal>/` layout remain readable in place; pairmux never migrates them implicitly.

## The envelope schema

Two fields are always present — `schema` and `ok`. Everything else is omitted when empty.

| field | type | meaning |
|-------|------|---------|
| `schema` | string | Always `"pairmux.v1"`. Every response is self-describing. |
| `ok` | bool | `true` on success, `false` on error. |
| `status` | string | Result state (see [Statuses](#statuses)). |
| `terminal` | string | The terminal the command acted on. |
| `mode` | string | Stored completion-detection mode: `hooks` or `sentinel`. `hooks-no-C` is a `doctor` diagnostic tier, not an envelope mode. |
| `exit_code` | int | The command's exit code (`run`, on `done`). |
| `duration_ms` | int | Wall-clock duration in milliseconds (`run`, on `done`). |
| `output` | string | Shaped terminal output: carriage-returns collapsed and ANSI stripped. `run` and `log --cmd` also drop the echoed command. Non-terminal commands use it for text results, including the update's old/new version, source index, and installed path. |
| `truncated` | object | Present when `output` was elided; see below. |
| `terminals` | array | The `ls` listing (one object per terminal). |
| `notes` | array of string | Unseen human messages left via `pairmux note`. |
| `next` | array of string | Ordered contextual hints. Obey prose safety entries, replace placeholders, then run the first applicable command. |
| `error` | object | Present when `ok` is `false`; see [Errors](#errors). |

**`truncated`** points at the rest of the output so it is never lost silently:

```bash
pairmux --json run build "seq 300"
```

```json
"truncated":{"omitted_lines":50,"get_full":"pairmux log build --cmd 3"}
```

- `omitted_lines` — how many lines were dropped by output shaping within the bytes that were read.
- `omitted_bytes` — how many raw bytes precede a bounded journal view. Omitted when zero.
- `get_full` — an executable command that returns the complete selected output.

For example, a byte-capped `peek` can report:

```bash
pairmux --json peek build
```

```json
"truncated":{"omitted_lines":0,"omitted_bytes":131072,"get_full":"pairmux log build --range 1:end"}
```

### Statuses

Terminal statuses are returned directly by `peek`, appear in each `ls` row, and are surfaced by `run`/`wait` when relevant:

| status | meaning |
|--------|---------|
| `idle` | Shell at a prompt, nothing running. |
| `running` | A command is executing. |
| `awaiting-input` | Running but quiet, last line looks like an interactive prompt. |
| `dead` | The pane is gone; the journal is retained. |
| `unknown` | The pane is live but pairmux cannot yet derive a confident state, for example during fresh sentinel activity or when the journal cannot be read. Usually transient; observe again before writing. |

Per-command action statuses are separate from terminal state. An interactive-shell `new` returns `created`; `new --cmd` immediately returns the program terminal's derived `running` or `dead` state. `run` returns `done`, `running`, or `awaiting-input`; `peek` returns the derived terminal status; `send`, `note`, and `kill` return `sent`, `noted`, and `killed`. The outer `log`/`ls`/`version` status is `ok`; `doctor` returns `ok` or `issues`; `skill install` returns `installed` or `dry-run`; and `update` (v0.6.0+) returns `updated` or `refreshed`. `wait` outcomes are `idle`, `awaiting-input`, `pattern-found`, `human-done`, `dead`, or `timeout`.

### Errors

An error envelope has `ok:false`, `status:"error"`, and an `error` object with a stable `code`, a `message`, and a `hint` (mirrored into `next`).

```bash
pairmux --json peek nonexistent
```

```json
{"schema":"pairmux.v1","ok":false,"status":"error","next":["pairmux ls"],"error":{"code":"E_NO_TERMINAL","message":"no terminal \"nonexistent\"; existing: build, deploy, dev","hint":"pairmux ls"}}
```

| `error.code` | when |
|--------------|------|
| `E_NO_TERMINAL` | The named terminal does not exist (the message lists the ones that do). |
| `E_EXISTS` | `new` was asked for a name already in use. |
| `E_BUSY` | Another writer holds the terminal's lock, or a prior command is still running. |
| `E_DEAD` | The terminal's pane is gone. |
| `E_BAD_ARGS` | A usage or flag error (invalid name/socket/key, bad regex, wrong terminal kind, invalid `PAIRMUX_BACKEND`). |
| `E_TMUX` | The terminal backend — tmux or the built-in server — failed. The name is historical; built-in failures carry built-in-specific hints (for example, set `PAIRMUX_TMPDIR`). |
| `E_UPDATE` | v0.6.0+: persistent uv ownership, supported version, uv execution, or post-update verification failed, or the platform is Windows. Follow the recovery hint. |
| `E_INTERNAL` | An unexpected internal error. |

---

## Agent commands

### new

Create a terminal: open a pane on the selected backend (a tmux window wired to the journal with `pipe-pane`, or a pane on the built-in server, which journals from the first byte). Terminals default to 220x50. Interactive terminals receive shell integration and wait for a ready prompt; `--cmd` terminals launch the requested program and wait only for initial output to quiesce.

```text
pairmux new [--name N] [--cwd D] [--cmd "..."]
```

- `--name N` — terminal name (matches `^[a-z0-9][a-z0-9_-]{0,31}$`). Auto-generated when omitted.
- `--cwd D` — working directory (defaults to the current directory).
- `--cmd "..."` — run a program instead of an interactive shell. Such a terminal is driven with `send`/`peek`, not `run`. On Windows the command runs through the terminal's shell: PowerShell `-EncodedCommand`, `cmd /s /c`, or `bash -lc` for Git Bash.

```bash
pairmux --json new --name build
```

```json
{"schema":"pairmux.v1","ok":true,"status":"created","terminal":"build","mode":"hooks","next":["pairmux run build \"echo hello\""]}
```

A program terminal reports its live derived state instead of `created`:

```bash
pairmux --json new --name prog --cmd "cat"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"prog","mode":"sentinel","next":["pairmux peek prog","pairmux send prog --text ... --enter"]}
```

### run

Send a command and **block until it completes, a quiet interactive prompt appears, or `--timeout` elapses**. Returns shaped output; a completed command also carries its exit code and duration. Takes the per-terminal writer lock.

```text
pairmux run <name> "<cmd>" [--timeout 60s] [--head 50] [--tail 200]
```

- The command is **one quoted argument**, matching the MCP tool contract. Passing multiple command
  tokens is an `E_BAD_ARGS` error whose hint shows the correctly quoted form — quoting your own
  shell consumed (`run t git commit -m "two words"`) can never be silently re-split.
- `--timeout` — Go duration (e.g. `90s`, `5m`). Default `60s`. If the deadline arrives without completion or a recognized prompt, the reply is `status: running`, not an error.
- `--head N` / `--tail N` — how many leading/trailing lines to keep (defaults 50 / 200). Middle lines are elided with a `truncated` pointer.

Completed:

```bash
pairmux --json run build "echo hello world"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"hello world"}
```

Timed out (no completion observed) — this is **not** an error; pairmux returns `status: running`
without killing the command, plus the current tail and ways to observe it:

```bash
pairmux --json run build "make -j4" --timeout 5s
```

```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"build","mode":"hooks","next":["pairmux peek build","pairmux log build --cmd 1"]}
```

Truncated output (a 300-line command with the default head/tail):

```bash
pairmux --json run build "seq 300"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"1\n2\n3\n...\n50\n…\n101\n...\n300","truncated":{"omitted_lines":50,"get_full":"pairmux log build --cmd 3"}}
```

Blocked on a prompt instead of finishing — `run` reports `awaiting-input`:

```bash
pairmux --json run deploy "printf 'Do you want to continue? [Y/n] '; read answer"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"deploy","mode":"hooks","output":"\nDo you want to continue? [Y/n] ","next":["pairmux send deploy --text <answer> --enter"]}
```

:::note
Only one command runs per terminal at a time. Sending a command while a prior one is still running returns `E_BUSY` (`"a command is still running"`). `run` refuses a command containing a newline — use `send` for interactive input.
:::

### peek

Show recent output and the derived status **without blocking, without taking the lock, and without recording anything**. Safe to call any number of times, from any number of agents.

```text
pairmux peek <name> [--screen | --tail N]
```

- (default) — read at most the final 64 KiB of the raw journal, shape it, and return its tail.
  `--tail N` sets how many lines (default 60). A skipped byte prefix is reported as
  `truncated.omitted_bytes`, with `pairmux log NAME --range 1:end` as the recovery command.
- `--screen` — a live render of the current viewport (useful for full-screen TUIs): tmux `capture-pane -p` on the tmux backend, and the built-in virtual screen with the same semantics on the built-in backend.

```bash
pairmux --json peek build
```

```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","output":"...\n300\n\nuser@host pairmux % ","truncated":{"omitted_lines":253,"omitted_bytes":65536,"get_full":"pairmux log build --range 1:end"},"next":["pairmux run build \"echo hello\""]}
```

### wait

Block until the terminal satisfies a condition. Read-only: records no events and takes no lock, so a human and an agent can both wait on the same terminal.

```text
pairmux wait <name> [--idle MS] [--pattern RE] [--done] [--human] [--notify] [--timeout 300s]
```

- `--idle MS` — after `MS` milliseconds of output silence (default 800ms), refresh state and resolve
  only when the shell is truly `idle`; quiet running commands keep waiting, while prompts and dead
  panes return their real status.
- `--pattern RE` — resolve when output appended **after this wait starts** matches the RE2 regex `RE`.
  Existing journal content never satisfies a new pattern wait.
- `--done` — resolve with `status: done` and the `exit_code` when the running command finishes, or
  when the next one does if the terminal is idle. Shell terminals only: a `--cmd` program terminal
  emits no completion marks and returns `E_BAD_ARGS`.
- `--human` — hand off to a human. Resolves on a `note` (or an unseen one already waiting, returned
  in `output`), and on the human being finished: the prompt is answered and the terminal is visibly
  moving again (`status: running`, whose `next` offers `--done` for following the rest), or the
  command finished outright (`status: done` with its `exit_code`). A prompt never resolves a
  `--human` wait, and a re-prompt after a rejected answer keeps it blocked. No `output` is returned
  for those two outcomes — see the note below.
- `--notify` — fire a desktop notification to summon a human (best-effort: `osascript` on macOS, `notify-send` on Linux, Windows PowerShell on Windows).
- `--timeout` — overall deadline (default `300s`). First condition satisfied wins. A `timeout` is an
  outcome, not an error: its `next` repeats the *same* wait — every condition flag preserved — with
  double the deadline, floored at `300s` for a handoff, because a handoff is paced by a person.

Because `wait` takes no lock and records nothing, any number of agents can hold a `--done` wait on
one terminal and every one of them wakes on the same completion mark. That is the whole subscription
mechanism — there is nothing to register with, and no daemon involved. It also covers a command a
human typed into the pane directly.

:::caution A resolved handoff returns no output
`--human` exists because a human is typing something the agent must not see. The span the command
occupied is exactly that span, so `wait --human` never quotes it back — it returns the fact and the
exit code. Use `peek` or `log` if the output is needed. (`--done` is not a handoff and does return
the tail, like `run`.)
:::

If the pane is already dead when `wait` starts, the command returns `E_DEAD`. The successful
`status: dead` outcome means the pane disappeared while the wait was in progress.

Idle:

```bash
pairmux --json wait build --idle 800
```

```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","next":["pairmux peek build","pairmux run build \"...\""]}
```

Pattern found (returns the matching line with context):

```bash
pairmux --json wait dev --pattern "listening on port"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"pattern-found","terminal":"dev","mode":"hooks","output":"\nnow listening on port 3000","next":["pairmux peek dev"]}
```

Human done (the note text is the output):

```bash
pairmux --json wait dev --human
```

```json
{"schema":"pairmux.v1","ok":true,"status":"human-done","terminal":"dev","mode":"hooks","output":"the token is fixed, go ahead","next":["pairmux peek dev"]}
```

Handoff resolved without a note — the human answered the prompt in the pane and the migration is
running again. This says *execution resumed*, not *the command finished*: a password answered at
second two can be followed by a five-minute migration, and the agent is released at second two.

```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"dbmigrate","mode":"hooks","next":["pairmux wait dbmigrate --done","pairmux peek dbmigrate"]}
```

Nobody has answered yet — not a failure, and not a cue to act:

```json
{"schema":"pairmux.v1","ok":true,"status":"timeout","terminal":"dbmigrate","mode":"hooks","next":["the human has not answered yet — do NOT type the secret","pairmux wait dbmigrate --human --notify --timeout 10m0s","pairmux peek dbmigrate"]}
```

Subscribed to a command another agent started:

```bash
pairmux --json wait build --done --timeout 600s
```

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"output":"\nBUILD SUCCESSFUL","next":["pairmux peek build"]}
```

### send

Deliver raw input to the program in a terminal: text, then keys, then Enter (in that order). Does not take the write lock, so it can answer a running program.

```text
pairmux send <name> [--text S] [--key K ...] [--enter]
```

- `--text S` — text delivered literally (via `send-keys -l` on the tmux backend); pairmux does no
  further shell expansion or key interpretation. The shell launching pairmux still expands its own arguments, so
  quote them when necessary (for example, `--text '$HOME'` sends the six literal characters).
- `--key K` — a named key; repeatable. Allowed: `Enter Escape Tab Space Up Down Left Right Home End PPage NPage BSpace DC`, `F1`–`F12`, `C-a`..`C-z`, `M-a`..`M-z`.
- `--enter` — append a final Enter.

At least one of `--text` / `--key` / `--enter` is required.

```bash
pairmux --json send deploy --text y --enter
```

```json
{"schema":"pairmux.v1","ok":true,"status":"sent","terminal":"deploy","mode":"hooks","next":["a command is running; sent input goes to it","pairmux peek deploy"]}
```

### log

Read a recent view or explicitly select complete history from the journal. The four modes are mutually exclusive.

```text
pairmux log <name> [--cmd N | --grep RE | --range A:B|A:end]
```

- (default) — a bounded recent view: read at most the final 4 MiB of raw journal, then return the
  last 500 shaped lines. A skipped byte prefix is reported with `omitted_bytes` and an executable
  `pairmux log NAME --range 1:end` recovery.
- `--cmd N` — the complete, unbounded output region of recorded command number `N`.
- `--grep RE` — every matching line from the complete shaped journal, prefixed with its 1-based line number.
- `--range A:B` / `--range A:end` — a complete 1-based inclusive shaped-line range; `end` means the final line.

The explicit selectors can return large replies because they intentionally do not apply the default byte or match caps.

```bash
pairmux --json log build --cmd 1
```

```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminal":"build","mode":"hooks","output":"\nhello world"}
```

```bash
pairmux --json log build --grep "hello"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminal":"build","mode":"hooks","output":"6:hello world\n9:hello world"}
```

### ls

List every terminal with its derived status, mode, current command, lock holder, unseen-note count, and last-activity time.

```text
pairmux ls
```

```bash
pairmux --json ls
```

```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminals":[{"name":"build","status":"idle","mode":"hooks","last_activity":"2026-07-18T15:13:38Z"},{"name":"web","status":"idle","mode":"hooks","last_activity":"2026-07-18T15:13:37Z"}]}
```

The text form has four columns. A lock holder, unseen-note count, and RFC 3339 last-activity time are appended to `CMD` when present:

```text
ok
  NAME       STATUS          MODE   CMD
  build      running         hooks  sleep 8  [lock:22018]  (2026-07-20T09:14:03Z)
  dbmigrate  awaiting-input  hooks  read -s password  [notes:1]  (2026-07-20T09:14:00Z)
  dev        idle            hooks  -  (2026-07-20T09:13:58Z)
```

### kill

Kill a terminal (or, with `--all`, every managed terminal). **Journals are retained on disk** either way. On Windows, killing a terminal ends its whole process tree through a job object.

```text
pairmux kill <name> | --all
```

```bash
pairmux --json kill deploy
```

```json
{"schema":"pairmux.v1","ok":true,"status":"killed","terminal":"deploy","next":["journal retained under the pairmux state namespace","pairmux prune deploy reclaims its disk when the history is no longer needed","pairmux ls"]}
```

### prune

Remove retained journals whose terminals are gone: state directories of **dead** terminals plus
`.prev` archives left when a terminal name was reused. This is the documented exit for `kill`'s
retain-the-journal default — pruned history is unrecoverable, so read or export anything you still
need first.

```text
pairmux prune [name] [--older-than 7d] [--dry-run]
```

- Without a name, sweeps the current endpoint namespace: every dead terminal's directory and every
  `.prev` archive (including orphaned archives that `ls` cannot show).
- With a name: a dead terminal loses its journal and archive; a **live** terminal loses only its
  `.prev` archive. A live terminal with no archive returns `E_BUSY` (`kill` it first).
- `--older-than` — only prune directories whose last activity (raw.log mtime) is older. Accepts Go
  durations plus whole-day shorthand (`7d`).
- `--dry-run` — list what would be pruned, remove nothing.
- A directory whose write lock is currently held is always kept and reported.

```bash
pairmux --json prune --older-than 7d
```

```json
{"schema":"pairmux.v1","ok":true,"status":"pruned","output":"byebye  4KB  pruned\nrotated.prev  312MB  pruned\npruned 2 of 2, 312MB","next":["pairmux ls"]}
```

`doctor` reports total retained bytes and the largest terminal directories, and suggests `prune`
when the state root has outgrown the large-journal guard.

---

## Human commands

### attach

Put a human in the live pane. Needs an interactive terminal on stdin and stdout. Attaching records
no journal event, so a `note` is the explicit, durable hand-back signal — though a `wait --human`
also ends on its own once what the human was summoned for resolves (the command completes, or the
prompt clears). On either backend, `send` warns the agent while a human is attached.

```text
pairmux attach [name]
```

**tmux backend.** Hands the human a live tmux client on the pairmux session, focusing the named
window first; the pairmux process is replaced with tmux. Detach with tmux's configured binding
(default `Ctrl-b d`). Refuses when already inside tmux: a human already in tmux must detach to an
outer shell or use another terminal, because `switch-client` cannot cross from another tmux server
to pairmux's named socket.

**Built-in backend.** Attaches *this* terminal straight to the pane — no session or window list, no
status bar — on the alternate screen, so the human's own scrollback is preserved.

| keys | effect |
|------|--------|
| `Ctrl-b d` | detach (the same key as tmux) |
| `Ctrl-b Ctrl-b` | send a literal `Ctrl-b` to the program |
| `Ctrl-b` + any other key | dropped |

- With no name, opens the only live terminal; with several, fails with `E_BAD_ARGS` listing them.
- Works inside tmux (there is no nesting refusal), but refuses to attach a pane to itself.
- While attached, the pane takes the human's terminal size; with several humans attached, the most
  recent attach or resize sets it. It returns to 220x50 when the last human detaches.
- On detach it prints `[detached from NAME] tell the agent what you did: pairmux note NAME "..."`;
  if the program exits while attached, it prints the exit message instead.

### watch

Render a self-refreshing dashboard of every terminal until `Ctrl-C`. Awaiting-input rows are flagged `!!`, dead rows `xx`.

```text
pairmux watch [--interval 2s]
```

### note

Record a message for the agent driving a terminal. Unseen messages appear in the `notes` field of a subsequent `run` or `peek` envelope. `wait --human` instead resolves with the note text in `output`; an ordinary idle/pattern wait does not add a `notes` field. Native `attach` records nothing, so `note` is the explicit hand-back signal — the one way to tell the agent *what* you did rather than merely that it can carry on.

```text
pairmux note <name> <text...>
```

```bash
pairmux --json note build "use the staging token, not prod"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"noted","terminal":"build","next":["the agent sees this note in its next run/peek, or as output from pairmux wait build --human"]}
```

The note then surfaces on a subsequent `run` or `peek`:

```bash
pairmux --json run build "echo resumed"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"resumed","notes":["use the staging token, not prod"]}
```

### doctor

Probe the environment. The first check, `backend`, names the selected backend, why it was chosen
(for example `tmux is installed` or `tmux is not installed`), and the tmux version or the built-in
server's socket path; it notes `tmux not installed (not required)` when relevant. The remaining
checks cover state-namespace writability, per-shell completion tier (probed on an isolated throwaway
endpoint of the selected backend), and the notification backend. A completed probe keeps `ok:true` and uses
`status:"ok"` when every required check passes or `status:"issues"` with a fix hint otherwise.
Invalid arguments and invalid socket configuration still return a normal error envelope.

```text
pairmux doctor
```

The report is environment-dependent: it prints the absolute endpoint-specific state namespace and
only the shells available to the probe. A healthy text response begins with `ok`; a completed probe
with failed checks begins with `issues` and lists the corresponding fixes.

`hooks-no-C` is a diagnostic tier for shells (notably bash 3.2, and PowerShell on Windows) that emit
OSC 133 A/D marks but no C mark; those terminals still store `mode:"hooks"`. Fish 4+ supplies OSC 133 natively. If fish emits
no ready mark, `new` degrades it to `mode:"sentinel"` and uses fish's `$status` in the marker.

### version

Print the build version.

```bash
pairmux --json version
```

```json
{"schema":"pairmux.v1","ok":true,"status":"ok","output":"0.1.0-dev"}
```

### update (v0.6.0+)

Update **only the currently running, verified persistent uv tool installation** of pairmux,
including one created by the Bash/WSL installer. No tmux installation or terminal access is needed.

**Not on Windows.** On Windows the command refuses with `E_UPDATE` before touching anything. uv
installs Windows tools as copied `.exe` files rather than the symlinked layout these ownership checks
rely on, and Windows cannot replace an executable while it runs. The hint says to close pairmux and
run `uv tool install --upgrade pairmux` (or `pip install -U pairmux` in the environment that has
it). Managed terminals may stay open: the built-in server runs from its own copy of the executable.

```text
pairmux update
```

There are no command-specific flags or positional arguments; global `--json` is supported. No
force, version-selection, source-selection, bootstrap, sudo, or automatic-update mode is provided.
The command never installs or updates uv, edits shell profiles, or touches tmux terminals. It does
not convert or change Homebrew, RPM, pipx/pip, manual/development installs, or temporary uvx runs.

**v0.5.3 and older lack this command.** First use `uv tool install --upgrade pairmux` with normal
uv configuration to obtain v0.6.0+ when available. Other installation types should use their own
manager or manual replacement, not this command.

**Ownership checks.** The resolved running executable, its regular non-symlink `uv-receipt.toml`,
and `uv tool list --show-paths` must agree on one persistent pairmux tool and its single entrypoint.
The entrypoint must be a symlink resolving to that executable. Verified custom tool/bin directories
are retained, not inherited `UV_TOOL_DIR`/`UV_TOOL_BIN_DIR` guesses. Missing, malformed, oversized,
ambiguous, escaped/multiline-path, or control-character receipt/listing data fails closed. Paths
containing ` (` are unsupported because uv's unquoted name/path display would be ambiguous. The
receipt and entrypoint are checked again before reinstalling; these checks are not a guarantee
against all races or a network-free/write-free preflight (uv's list query may take its tool-root
lock).

**Source and version policy.** uv must already be installed. The updater clears every inherited
`UV_*` setting, ignores uv configuration, and reinstalls only wheels from
**`https://pypi.org/simple`**, with persistent cache disabled and no source builds. It replaces old
exact pins, constraints, indexes, and extras rather than retaining them. This differs from an
ordinary `uv tool install pairmux`, which uses normal environment/configuration. uv may separately
obtain managed Python from Astral upstream; that download is not from PyPI.

The selected wheel is the latest compatible **stable** release at or above the running version's
numeric core. Recognized alpha/beta/rc versions can advance to their core's final or a later stable
release, never an older stable core. Dev/snapshot or unrecognized versions are refused. After uv
succeeds, the command runs the verified installed executable's `version` to check the result.

**Result and recovery.** A version change returns `updated`; a same-version reinstall returns
`refreshed`, not a no-op. `output` contains old/new versions, the fixed source index, and the
absolute installed entrypoint; no new envelope fields are added. For example, a same-version run
after v0.6.0 is available:

```bash
pairmux --json update
```

```json
{"schema":"pairmux.v1","ok":true,"status":"refreshed","output":"pairmux 0.6.0 -> 0.6.0\nsource: https://pypi.org/simple\ninstalled: /home/alice/.local/bin/pairmux"}
```

Ownership, unsupported-version, missing-uv, subprocess, and post-update verification failures emit
`ok:false`, `status:"error"`, and `error.code:"E_UPDATE"` with an actionable `error.hint` mirrored
into `next`. Automatic rollback is not guaranteed; inspect `pairmux version` and use the original
manager, or manually recover a uv install with `uv tool install --upgrade pairmux` (normal uv
configuration applies). The updater never bootstraps uv as error recovery. For example, if uv is
missing:

```json
{"schema":"pairmux.v1","ok":false,"status":"error","next":["Install uv from https://docs.astral.sh/uv/getting-started/installation/ and retry pairmux update."],"error":{"code":"E_UPDATE","message":"uv was not found on PATH or in ~/.local/bin","hint":"Install uv from https://docs.astral.sh/uv/getting-started/installation/ and retry pairmux update."}}
```

Exit codes are **0** for `updated`/`refreshed`, **1** for `E_UPDATE`, and **2** for invalid arguments
(`E_BAD_ARGS`). uv's subprocess output is captured rather than mixed into the single JSON envelope.

### skill install

Install the embedded canonical Agent Skill. A named target creates its skill directory; `all` only
installs into agent configuration directories that already exist and reports the rest as skipped.

```text
pairmux skill install [--target T|all] [--dry-run]
```

Targets: `claude-code`, `codex`, `gemini`, `cursor`, `opencode`, `copilot`, `windsurf`, `kiro`,
`amp`, and the cross-agent `agents` directory. See [Agent Skills](./skills.md) for paths and evals.

### mcp serve

Serve typed pairmux tools over the newline-delimited MCP stdio transport. The server implements MCP
protocol revision `2025-11-25`; stdout is reserved for JSON-RPC messages and diagnostics go to stderr.

```text
pairmux mcp serve
```

Configure an MCP client to launch `pairmux` directly with arguments `["mcp", "serve"]`. `tools/list`
advertises `pairmux_new`, `pairmux_run`, `pairmux_peek`, `pairmux_wait`, `pairmux_send`, `pairmux_log`,
`pairmux_ls`, `pairmux_kill`, `pairmux_note`, and `pairmux_doctor`, each with a closed JSON Schema.
`skill install` is intentionally not exposed because it writes agent configuration.

Every tool invokes this same executable with an argv array and `--json`; no wrapper shell is involved.
When that subprocess returns a valid `pairmux.v1` envelope, the envelope is exposed in
`structuredContent` and duplicated as JSON text for compatibility. An envelope with `ok:false`
becomes a tool result with `isError:true`. Schema validation failures, executor/capture failures,
malformed MCP requests, and unknown tools are MCP-level errors and do not carry a CLI envelope. Tool
annotations conservatively flag command execution, input, and terminal kills as potentially
destructive so clients can request user approval.

The server caps each tool subprocess's stdout at 1 MiB and stderr at 64 KiB. If either stream exceeds
its limit, the call returns an actionable tool error instead of forwarding partial output. Narrow a
large `pairmux_log` call with `command_id`, `grep`, or a smaller `range`; the underlying journal
remains complete.

See the official [MCP lifecycle](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle),
[stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports), and
[tool result semantics](https://modelcontextprotocol.io/specification/2025-11-25/server/tools).
