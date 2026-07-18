# pairmux command reference

Every command speaks the `pairmux.v1` envelope. Add `--json` (before or after the command) for the
one-line machine form shown here; without it you get a friendly text block. Global flags:

| flag | meaning |
|------|---------|
| `--json` | emit a compact JSON envelope instead of the text block |
| `--socket S` | use tmux socket `S` (isolates a set of terminals); overrides `$PAIRMUX_SOCKET` |
| `--` | end global-flag parsing, for a command that itself contains `--json`/`--socket` |

Environment: `PAIRMUX_SOCKET` sets the default socket; `PAIRMUX_STATE_DIR` sets the journal root
(default `~/.local/state/pairmux`).

## The envelope schema

`schema` and `ok` are always present; everything else is omitted when empty.

| field | type | meaning |
|-------|------|---------|
| `schema` | string | always `"pairmux.v1"` |
| `ok` | bool | `true` on success, `false` on error |
| `status` | string | result state (see below) |
| `terminal` | string | the terminal acted on |
| `mode` | string | completion-detection mode: `hooks` or `sentinel` |
| `exit_code` | int | the command's exit code (`run`, on `done`) — this is the **command's** code, not pairmux's |
| `duration_ms` | int | wall-clock duration (`run`, on `done`) |
| `output` | string | shaped output: CR collapsed, ANSI stripped, echoed command dropped |
| `truncated` | object | present when `output` was elided: `{omitted_lines, get_full}` |
| `terminals` | array | the `ls` listing (one object per terminal) |
| `notes` | array of string | unseen human messages left via `pairmux note` — read and obey them |
| `next` | array of string | concrete next-step commands; the first is your next move |
| `error` | object | present when `ok:false`: `{code, message, hint}` |

**Statuses.** Terminal states: `idle`, `running`, `awaiting-input`, `dead`. Per-command action
statuses: `created` (`new`), `done`/`running` (`run`), `sent` (`send`), `noted` (`note`),
`killed` (`kill`), `ok` (`peek`/`log`/`ls`/`doctor`/`version`), and `wait`'s outcomes
`idle` / `pattern-found` / `human-done` / `timeout`.

**Error codes** (envelope has `ok:false`, `status:"error"`, and an `error` object):

| code | when |
|------|------|
| `E_NO_TERMINAL` | the named terminal does not exist (message lists the ones that do) |
| `E_EXISTS` | `new` asked for a name already in use |
| `E_BUSY` | another writer holds the lock, or a prior command is still running |
| `E_DEAD` | the terminal's pane is gone |
| `E_BAD_ARGS` | usage/flag error (invalid key, bad regex, secret prompt, wrong terminal kind) |
| `E_TMUX` | an underlying tmux command failed |
| `E_INTERNAL` | an unexpected internal error |

---

## Agent commands

### new — create a terminal
```text
pairmux new [--name N] [--cwd D] [--cmd "..."]
```
- `--name N` — name matching `^[a-z0-9][a-z0-9_-]{0,31}$`; auto-generated if omitted.
- `--cwd D` — working directory (defaults to the current directory).
- `--cmd "..."` — launch a program instead of an interactive shell. **Drive such a terminal with
  `send`/`peek`, not `run`** (it reports `mode:"sentinel"`).
```json
{"schema":"pairmux.v1","ok":true,"status":"created","terminal":"build","mode":"hooks","next":["pairmux run build \"echo hello\""]}
```

### run — send a command and block until it completes or times out
```text
pairmux run <name> <cmd...> [--timeout 60s] [--head 50] [--tail 200]
```
- `--timeout` — Go duration (`90s`, `5m`); default `60s`. **On timeout the reply is `status:"running"`,
  not an error** — it carries the tail and a `next` for continuing to wait.
- `--head N` / `--tail N` — leading/trailing lines to keep (defaults 50 / 200); the middle is elided
  with a `truncated` pointer.
- Refuses a command containing a newline (use `send` for interactive input). Takes the writer lock.

Done, and with a non-zero exit the `output` field may be absent (nothing was printed):
```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":1,"duration_ms":101}
```
Timed out (still running — an outcome, not an error):
```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"build","mode":"hooks","output":"\nstarting","next":["pairmux peek build","pairmux log build --cmd 1"]}
```
Truncated (a 300-line command, default head/tail):
```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"1\n2\n…\n50\n…\n101\n…\n300","truncated":{"omitted_lines":50,"get_full":"pairmux log build --cmd 3"}}
```

### wait — block until a condition
```text
pairmux wait <name> [--idle MS] [--pattern RE] [--human] [--notify] [--timeout 300s]
```
- `--idle MS` — resolve when the journal has been quiet for `MS` ms (default condition, 800ms).
  Note: fires on *any* silence, so it can resolve while a command is merely paused (e.g. sleeping).
- `--pattern RE` — resolve when **new** output (produced after the wait starts) matches the RE2 regex.
  It does **not** scan output already printed before the wait — see troubleshooting.
- `--human` — resolve when a human leaves a `note` (or one is already waiting).
- `--notify` — best-effort desktop notification to summon a human.
- `--timeout` — overall deadline (default `300s`); first condition satisfied wins.

Read-only: records nothing, takes no lock, so a human and an agent can both wait on one terminal.
```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","next":["pairmux peek build","pairmux run build \"...\""]}
```
```json
{"schema":"pairmux.v1","ok":true,"status":"human-done","terminal":"dev","mode":"hooks","output":"the token is fixed, go ahead","next":["pairmux peek dev"]}
```
```json
{"schema":"pairmux.v1","ok":true,"status":"timeout","terminal":"api","mode":"hooks","next":["pairmux peek api","pairmux wait api --timeout 16s"]}
```

### peek — recent output + status, no blocking, no lock
```text
pairmux peek <name> [--screen | --tail N]
```
- default: the shaped journal tail (`--tail N` sets line count, default 60).
- `--screen`: a live `capture-pane` render of the current viewport (useful for full-screen TUIs).

Safe to call any number of times from any number of agents. Surfaces `notes`.
```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","output":"…\n300\n\nuser@host % ","truncated":{"omitted_lines":253,"get_full":"pairmux log build"},"next":["pairmux run build \"echo hello\""]}
```

### send — deliver raw input to a running program
```text
pairmux send <name> [--text S] [--key K ...] [--enter]
```
Applied in order: text, then keys, then a trailing Enter. Does **not** take the write lock, so it can
answer a program a prior `run` is still blocked on.
- `--text S` — literal text via `send-keys -l` (no shell expansion, no key interpretation).
- `--key K` — a named key, repeatable. Valid: `Enter Escape Tab Space Up Down Left Right Home End
  PPage NPage BSpace DC`, `F1`–`F12`, `C-a`..`C-z`, `M-a`..`M-z`. A plain letter like `--key q` is
  **rejected** — use `--text q` to type the character.
- `--enter` — append a final Enter. At least one of `--text`/`--key`/`--enter` is required.
```json
{"schema":"pairmux.v1","ok":true,"status":"sent","terminal":"deploy","mode":"hooks","next":["a command is running; sent input goes to it","pairmux peek deploy"]}
```

### log — full or filtered history from the journal
```text
pairmux log <name> [--cmd N | --grep RE | --range A:B]
```
The four modes are mutually exclusive. Resolves truncation and scrolled-off output without re-running.
- default: shaped journal tail (last 500 lines).
- `--cmd N` — the complete output of recorded command number `N`.
- `--grep RE` — RE2-matching lines, each prefixed with its line number (capped at 200 matches).
- `--range A:B` — the 1-based inclusive line range.
```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminal":"build","mode":"hooks","output":"6:hello world"}
```

### ls — list terminals + status
```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminals":[{"name":"build","status":"idle","mode":"hooks","last_activity":"2026-07-18T15:13:38Z"}]}
```
Text form is a table; a lock holder pid, pending command, and a `[notes:N]` badge show inline.

### kill — end terminal(s); journals are kept
```text
pairmux kill <name> | --all
```
```json
{"schema":"pairmux.v1","ok":true,"status":"killed","terminal":"deploy","next":["journal retained at ~/.local/state/pairmux/deploy","pairmux ls"]}
```

---

## Human commands (you rarely call these; a human does)

- `pairmux attach [name]` — hand the human a live tmux client on the session, focused on `name`.
  Refuses when already inside tmux or when stdout is not a terminal.
- `pairmux watch [--interval 2s]` — a self-refreshing dashboard until `Ctrl-C`. `!!` flags
  awaiting-input, `xx` flags dead.
- `pairmux note <name> <text...>` — record a message for the agent; surfaces in the agent's next
  `run`/`peek`/`wait` `notes`, and resolves `wait --human`.
- `pairmux doctor` — probe tmux version, state-dir writability, per-shell completion tier, notifier.
- `pairmux version` — print the build version.

## Completion-detection modes

| mode | how completion is detected |
|------|----------------------------|
| `hooks` | OSC 133 shell integration (bash/zsh) — precise start/end + exit code |
| `sentinel` | an injected `printf` marker carrying `$?` — fallback for other shells and `--cmd` programs |

`hooks` is chosen automatically for interactive shells; `--cmd` programs and unknown shells fall back
to `sentinel`. Both surface the same statuses; you don't choose the mode.
