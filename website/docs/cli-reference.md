---
title: CLI Reference
description: Every pairmux command with its flags, a real JSON example, and the full pairmux.v1 envelope schema.
---

# CLI Reference

```text
usage: pairmux [--json] [--socket S] <command> [args]
```

Every command replies through the **`pairmux.v1` envelope**. Add `--json` for the machine-readable one-line form (shown throughout this page); without it, pairmux prints a friendly text block. All JSON examples below are captured verbatim from the real binary.

## Global flags

| flag | meaning |
|------|---------|
| `--json` | Emit a compact JSON envelope instead of the text block. May appear before or after the command. |
| `--socket S` | Use tmux socket `S` (isolates a set of terminals). Overrides `$PAIRMUX_SOCKET`. |
| `--` | Ends global-flag parsing, for a command that itself contains `--json`/`--socket`. |

Environment: `PAIRMUX_SOCKET` sets the default socket; `PAIRMUX_STATE_DIR` sets the journal root (default `~/.local/state/pairmux`).

## The envelope schema

Two fields are always present — `schema` and `ok`. Everything else is omitted when empty.

| field | type | meaning |
|-------|------|---------|
| `schema` | string | Always `"pairmux.v1"`. Every response is self-describing. |
| `ok` | bool | `true` on success, `false` on error. |
| `status` | string | Result state (see [Statuses](#statuses)). |
| `terminal` | string | The terminal the command acted on. |
| `mode` | string | Completion-detection mode: `hooks` or `sentinel`. |
| `exit_code` | int | The command's exit code (`run`, on `done`). |
| `duration_ms` | int | Wall-clock duration in milliseconds (`run`, on `done`). |
| `output` | string | Shaped output: carriage-returns collapsed, ANSI stripped, echoed command dropped. |
| `truncated` | object | Present when `output` was elided; see below. |
| `terminals` | array | The `ls` listing (one object per terminal). |
| `notes` | array of string | Unseen human messages left via `pairmux note`. |
| `next` | array of string | Concrete next-step commands. Always present for non-final states and errors. |
| `error` | object | Present when `ok` is `false`; see [Errors](#errors). |

**`truncated`** points at the rest of the output so it is never lost silently:

```json
"truncated":{"omitted_lines":50,"get_full":"pairmux log build --cmd 3"}
```

- `omitted_lines` — how many lines were dropped from the middle.
- `get_full` — the exact command that returns the complete output (or a raw-journal path).

### Statuses

Terminal statuses (reported by `ls`, `peek`, `wait`):

| status | meaning |
|--------|---------|
| `idle` | Shell at a prompt, nothing running. |
| `running` | A command is executing. |
| `awaiting-input` | Running but quiet, last line looks like an interactive prompt. |
| `dead` | The pane is gone; the journal is retained. |

Per-command action statuses: `created` (`new`), `done` / `running` (`run`), `sent` (`send`), `noted` (`note`), `killed` (`kill`), `ok` (`peek`/`log`/`ls`/`doctor`/`version`), and `wait`'s outcomes `idle` / `pattern-found` / `human-done` / `timeout`.

### Errors

An error envelope has `ok:false`, `status:"error"`, and an `error` object with a stable `code`, a `message`, and a `hint` (mirrored into `next`).

```json
{"schema":"pairmux.v1","ok":false,"status":"error","next":["pairmux ls"],"error":{"code":"E_NO_TERMINAL","message":"no terminal \"nonexistent\"; existing: build, deploy, dev","hint":"pairmux ls"}}
```

| `error.code` | when |
|--------------|------|
| `E_NO_TERMINAL` | The named terminal does not exist (the message lists the ones that do). |
| `E_EXISTS` | `new` was asked for a name already in use. |
| `E_BUSY` | Another writer holds the terminal's lock, or a prior command is still running. |
| `E_DEAD` | The terminal's pane is gone. |
| `E_BAD_ARGS` | A usage or flag error (invalid key, bad regex, secret prompt, wrong terminal kind). |
| `E_TMUX` | An underlying tmux command failed. |
| `E_INTERNAL` | An unexpected internal error. |

---

## Agent commands

### new

Create a terminal: open a tmux window, wire `pipe-pane` into the journal, inject shell integration, and wait for the shell to become interactive.

```text
pairmux new [--name N] [--cwd D] [--cmd "..."]
```

- `--name N` — terminal name (matches `^[a-z0-9][a-z0-9_-]{0,31}$`). Auto-generated when omitted.
- `--cwd D` — working directory (defaults to the current directory).
- `--cmd "..."` — run a program instead of an interactive shell. Such a terminal is driven with `send`/`peek`, not `run`.

```bash
pairmux new --name build
```

```json
{"schema":"pairmux.v1","ok":true,"status":"created","terminal":"build","mode":"hooks","next":["pairmux run build \"echo hello\""]}
```

### run

Send a command and **block until it completes or `--timeout` elapses**. Returns the shaped output with exit code and duration. Takes the per-terminal writer lock.

```text
pairmux run <name> <cmd...> [--timeout 60s] [--head 50] [--tail 200]
```

- `--timeout` — Go duration (e.g. `90s`, `5m`). Default `60s`. On timeout the reply is `status: running`, not an error.
- `--head N` / `--tail N` — how many leading/trailing lines to keep (defaults 50 / 200). Middle lines are elided with a `truncated` pointer.

Completed:

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"hello world"}
```

Timed out (still running) — note this is **not** an error; it carries the tail and how to keep waiting:

```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"build","mode":"hooks","next":["pairmux peek build","pairmux log build --cmd 1"]}
```

Truncated output (a 300-line command with the default head/tail):

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"1\n2\n3\n...\n50\n…\n101\n...\n300","truncated":{"omitted_lines":50,"get_full":"pairmux log build --cmd 3"}}
```

Blocked on a prompt instead of finishing — `run` reports `awaiting-input`:

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

- (default) — the journal tail, shaped. `--tail N` sets how many lines (default 60).
- `--screen` — a live `capture-pane` render of the current viewport (useful for full-screen TUIs).

```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","output":"...\n300\n\nuser@host pairmux % ","truncated":{"omitted_lines":253,"get_full":"pairmux log build"},"next":["pairmux run build \"echo hello\""]}
```

### wait

Block until the terminal satisfies a condition. Read-only: records no events and takes no lock, so a human and an agent can both wait on the same terminal.

```text
pairmux wait <name> [--idle MS] [--pattern RE] [--human] [--notify] [--timeout 300s]
```

- `--idle MS` — resolve when the journal has been quiet for `MS` milliseconds (default condition, 800ms).
- `--pattern RE` — resolve when new output matches the RE2 regex `RE`.
- `--human` — resolve when a human leaves a `note` (or one is already waiting).
- `--notify` — fire a desktop notification to summon a human (best-effort).
- `--timeout` — overall deadline (default `300s`). First condition satisfied wins.

Idle:

```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","next":["pairmux peek build","pairmux run build \"...\""]}
```

Pattern found (returns the matching line with context):

```json
{"schema":"pairmux.v1","ok":true,"status":"pattern-found","terminal":"dev","mode":"hooks","output":"\nnow listening on port 3000","next":["pairmux peek dev"]}
```

Human done (the note text is the output):

```json
{"schema":"pairmux.v1","ok":true,"status":"human-done","terminal":"dev","mode":"hooks","output":"the token is fixed, go ahead","next":["pairmux peek dev"]}
```

### send

Deliver raw input to the program in a terminal: text, then keys, then Enter (in that order). Does not take the write lock, so it can answer a running program.

```text
pairmux send <name> [--text S] [--key K ...] [--enter]
```

- `--text S` — literal text via `send-keys -l` (no shell expansion or key interpretation).
- `--key K` — a named key; repeatable. Allowed: `Enter Escape Tab Space Up Down Left Right Home End PPage NPage BSpace DC`, `F1`–`F12`, `C-a`..`C-z`, `M-a`..`M-z`.
- `--enter` — append a final Enter.

At least one of `--text` / `--key` / `--enter` is required.

```bash
pairmux send deploy --text y --enter
```

```json
{"schema":"pairmux.v1","ok":true,"status":"sent","terminal":"deploy","mode":"hooks","next":["pairmux peek deploy"]}
```

### log

Read full or filtered history from the journal (resolves truncation and scrolled-off output). The four modes are mutually exclusive.

```text
pairmux log <name> [--cmd N | --grep RE | --range A:B]
```

- (default) — the shaped journal tail (last 500 lines).
- `--cmd N` — the complete output of recorded command number `N`.
- `--grep RE` — lines matching the RE2 regex, each prefixed with its line number (capped at 200 matches).
- `--range A:B` — the 1-based inclusive line range `A:B`.

```bash
pairmux log build --cmd 1
```

```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminal":"build","mode":"hooks","output":"\nhello world"}
```

```bash
pairmux log build --grep "hello"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminal":"build","mode":"hooks","output":"6:hello world\n9:hello world"}
```

### ls

List every terminal with its derived status, mode, current command, lock holder, unseen-note count, and last-activity time.

```text
pairmux ls
```

```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminals":[{"name":"build","status":"idle","mode":"hooks","last_activity":"2026-07-18T15:13:38Z"},{"name":"web","status":"idle","mode":"hooks","last_activity":"2026-07-18T15:13:37Z"}]}
```

The text form is a table; a lock holder and pending command are shown inline:

```text
   NAME       STATUS          MODE   LOCK   AGE  CMD
   build      running         hooks  22018  3s   sleep 8
!! dbmigrate  awaiting-input  hooks  -      6s   printf 'Password: '; read -s p; echo
   dev        idle            hooks  -      1s   -
```

### kill

Kill a terminal's window (or, with `--all`, every managed window). **Journals are retained on disk** either way.

```text
pairmux kill <name> | --all
```

```bash
pairmux kill deploy
```

```json
{"schema":"pairmux.v1","ok":true,"status":"killed","terminal":"deploy","next":["journal retained at ~/.local/state/pairmux/deploy","pairmux ls"]}
```

---

## Human commands

### attach

Hand the human a live tmux client on the pairmux session, focusing the named window first. Replaces the pairmux process with tmux. Refuses when already inside tmux or when stdout is not a terminal.

```text
pairmux attach [name]
```

### watch

Render a self-refreshing dashboard of every terminal until `Ctrl-C`. Awaiting-input rows are flagged `!!`, dead rows `xx`.

```text
pairmux watch [--interval 2s]
```

### note

Record a message for the agent driving a terminal. The agent sees it in the `notes` field of its next `run`/`peek`/`wait` envelope, and `wait --human` resolves on it.

```text
pairmux note <name> <text...>
```

```bash
pairmux note build "use the staging token, not prod"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"noted","terminal":"build","next":["the agent sees this note on its next run/peek/wait of build"]}
```

The note then surfaces on the agent's next command:

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"resumed","notes":["use the staging token, not prod"]}
```

### doctor

Probe the environment: tmux version, state-dir writability, per-shell completion tier (on an isolated throwaway socket), and the notification backend. Always exits `ok`; individual failures show as issues with a fix hint.

```text
pairmux doctor
```

```text
pairmux doctor

✓  tmux        3.7b at /opt/homebrew/bin/tmux (>= 3.2)
✓  state dir   writable: ~/.local/state/pairmux
✓  live probe  zsh: hooks;  bash: hooks-no-C;  dash: sentinel
✓  notifier    osascript at /usr/bin/osascript (macOS notifications available)
```

### version

Print the build version.

```bash
pairmux version
```

```json
{"schema":"pairmux.v1","ok":true,"status":"ok","output":"0.1.0-dev"}
```
