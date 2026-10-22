# pairmux command reference

## Contents

- [Invocation, backend and shell selection](#invocation-backend-and-shell-selection)
- [The envelope schema](#the-envelope-schema)
- [Agent commands](#agent-commands)
- [Human and maintenance commands](#human-and-maintenance-commands)
- [Completion-detection modes](#completion-detection-modes)

## Invocation, backend and shell selection

Ordinary CLI replies support `--json` (before or after the command) for compact `pairmux.v1`
envelopes; without it you get friendly text. Exceptions: `help` is plain text even with `--json`,
`attach`/`watch` are interactive human interfaces, and `mcp serve` speaks MCP JSON-RPC over stdio,
not CLI envelopes. Global flags:

| flag | meaning |
|------|---------|
| `--json` | emit a compact JSON envelope instead of the text block |
| `--socket S` | use endpoint `S` (tmux socket or built-in server name; isolates a set of terminals); overrides `$PAIRMUX_SOCKET` |
| `--` | end global-flag parsing, for a command that itself contains `--json`/`--socket` |

`PAIRMUX_BACKEND=auto` (or unset), from v1.0:

1. **Windows:** built-in ConPTY backend (Windows 10 1809+); no tmux required.
2. **Unix endpoint already running built-in:** keep using it, unless tmux is also available and a
   tmux server is running for that endpoint too; when both run, tmux wins.
3. **Otherwise:** available tmux 3.2+ → tmux; absent or known-too-old tmux → built-in PTYs.

Explicit `PAIRMUX_BACKEND=tmux` or `builtin` pins the backend; `tmux` on Windows is `E_BAD_ARGS`.
Terminals inherit the selected backend, keeping nested layers on it. `doctor` shows the choice/reason.
Each endpoint has an isolated hashed directory beneath `<state root>/.sockets/`, separately per backend;
equal names cannot share metadata, and `ls` lists only the selected backend's terminals.

**Shell precedence:** `PAIRMUX_SHELL` > `SHELL` > OS fallback. Unix falls back to `bash`.
Windows honors `SHELL` only when it names a real file (an exported `/usr/bin/bash` often does not);
its fallback is `pwsh.exe`, then `powershell.exe`, then `COMSPEC`, else `cmd.exe`. Write `run` commands
in the selected shell's syntax. PowerShell is a fallback, not an unconditional Windows shell.

Other environment variables:

- `PAIRMUX_SOCKET`: default endpoint. **Inside a pane this is its child endpoint**, not the endpoint
  that owns the pane. To note the enclosing terminal, use the creator-supplied owning `--socket`;
  see [the team/worker recipe](collaboration.md#notes-from-inside-a-pane-owning-socket-recipe).
- `PAIRMUX_STATE_DIR`: journal root; otherwise `$XDG_STATE_HOME/pairmux`, then `~/.local/state/pairmux`.
- `PAIRMUX_TMPDIR`: built-in runtime directory (socket, lock and `<socket>.log`); see troubleshooting.
- `PAIRMUX_SECRET_PROMPT_RE`: extra secret-prompt wording (RE2), validated by `doctor`.
- `PAIRMUX_NAME`, `PAIRMUX_STATE_NAMESPACE`, `PAIRMUX_TERMINAL_DIR`: pane identity and canonical state
  locators supplied by pairmux. They are not a parent-socket API; do not reverse-engineer one.

## The envelope schema

In CLI envelopes, `schema` and `ok` are always present; other fields are contextual (empty optional
fields are omitted, but zero `exit_code`/`duration_ms` values are meaningful).

| field | type | meaning |
|-------|------|---------|
| `schema` | string | always `"pairmux.v1"` |
| `ok` | bool | `true` on success, `false` on error |
| `status` | string | result state (see below) |
| `terminal` | string | the terminal acted on |
| `mode` | string | `hooks` or `sentinel`; program terminals also report `sentinel`, without shell completion support |
| `exit_code` | int | shell command's exit code on `done` (`run` / completion wait), not pairmux's process exit code |
| `duration_ms` | int | command duration when its start is known; can be absent for a subscriber |
| `output` | string | command-specific text: shaped terminal output, a report, or note text; not always terminal data |
| `truncated` | object | present when `output` was elided: `{omitted_lines, omitted_bytes?, get_full}` |
| `terminals` | array | the `ls` listing (one object per terminal) |
| `notes` | array of string | unseen messages from a human/agent via `note`; read and follow within task authorization |
| `next` | array of string | optional ordered hints: prose/safety, placeholders, and applicable commands |
| `error` | object | present when `ok:false`: `{code, message, hint}` |

**Statuses depend on the command, not just the terminal:**

| command / context | statuses |
|-------------------|----------|
| `new` shell | `created` |
| `new --cmd` program | `running` or `dead` (not `created`; inspect with `peek`) |
| `run` | `done`, `running`, `awaiting-input` |
| `peek` | derived terminal status: `idle`, `running`, `awaiting-input`, `dead` (not generic `ok`) |
| `ls` | top-level `ok`; each terminal row has its derived state |
| `wait` | requested-condition outcomes: `idle`, `awaiting-input`, `done`, `running`, `pattern-found`, `note`, `human-done`, `dead`, `timeout` |
| `send` / `note` / `kill` | `sent` / `noted` / `killed` |
| `log` / `version --json` | `ok` |
| `doctor` | `ok` or `issues`, both with `ok:true`; read the failed checks and fixes |
| `skill install` | `installed` or `dry-run` |
| `prune` | `pruned`, `ok` (dry-run/nothing to prune), or `issues` (partial failure) |
| `update` (v0.6.0+) | `updated` or `refreshed` |

`awaiting-input` can be inferred, especially in raw-mode TUIs: it is not proof of completion or safe
input. `human-done` means a note arrived, not that the command finished.

**Error codes** (envelope has `ok:false`, `status:"error"`, and an `error` object):

| code | when |
|------|------|
| `E_NO_TERMINAL` | the named terminal does not exist (message lists the ones that do) |
| `E_EXISTS` | `new` asked for a name already in use |
| `E_BUSY` | another writer holds the lock, or a prior command is still running |
| `E_DEAD` | the terminal's pane is gone |
| `E_BAD_ARGS` | usage/flag error (invalid name/key, bad regex, wrong terminal kind, invalid `PAIRMUX_BACKEND`) |
| `E_TMUX` | the terminal backend failed — tmux or the built-in server (historical name); see troubleshooting |
| `E_UPDATE` | unsupported/unverified installation, uv failure, post-update verification failure, or Windows |
| `E_INTERNAL` | an unexpected internal error |

---

## Agent commands

### new — create a terminal
```text
pairmux new [--name N] [--cwd D] [--cmd "..."]
```
- `--name N` — name matching `^[a-z0-9][a-z0-9_-]{0,31}$`; auto-generated if omitted.
- `--cwd D` — working directory (defaults to the current directory).
- `--cmd "..."` — launch a known REPL/TUI/server instead of a shell terminal. Returns `running` or
  `dead`, not `created`; initial output is read with `peek`/`log`. Drive it with `send`/`peek`, idle
  waits, future pattern waits, or explicit notes. `run` and `wait --done` reject program terminals
  with `E_BAD_ARGS`, even though they report `mode:"sentinel"`.
- Without `--cmd`, `new` creates a shell and returns `created`; sequential `run` calls share its state.
```json
{"schema":"pairmux.v1","ok":true,"status":"created","terminal":"build","mode":"hooks","next":["pairmux run build \"echo hello\""]}
```

### run — send a command and block until it completes or times out
```text
pairmux run <name> "<cmd>" [--timeout 60s] [--head 50] [--tail 200]
```
- **The command is one quoted argument** (same contract as the MCP tool). Multiple command tokens
  return `E_BAD_ARGS` with a hint showing the correctly quoted form — quoting your shell already
  consumed is never silently re-split.
- `--timeout` — Go duration (`90s`, `5m`); CLI default `60s` (not a startup recommendation).
  **On timeout the reply is `status:"running"`, not an error** — it carries the tail and a `next`
  for further observation. Submit the command, estimate time to its first meaningful signal rather
  than total runtime, and generally use an initial bounded observation of 30s, shorter for fast tasks
  (for example `pairmux run build "make -j4" --timeout 30s`). This bounded `run` counts as observation:
  inspect its returned output/status before adding a wait. If it already supplies enough evidence of
  completion or healthy startup, do not add a redundant wait.
- `--head N` / `--tail N` — leading/trailing lines to keep (defaults 50 / 200); the middle is elided
  with a `truncated` pointer.
- Refuses a command containing a newline (use `send` for interactive input). Takes the writer lock.
  Shell terminals only: `--cmd` programs reject `run`. Can return `awaiting-input` before timeout;
  answer safe prompts once or hand off secrets. `running` alone does not prove healthy startup:
  check returned output/status or a safe `peek` for expected progress/readiness, early errors or a
  prompt before unrelated work or a long wait. Unknown/silent is not healthy; investigate missing
  evidence rather than assuming success or automatically increasing the wait.
- If further observation is needed, prefer `wait <name> --done --idle 800 --timeout 30s` (shorter for
  fast signals) to race completion against verified idle/prompt detection. Event-aware bounded
  `run`/`wait` can return early; a brief sleep is only a fallback when no suitable blocking observation
  is available, followed by a safe check, never repeated blind polling. Credential handoffs are
  excluded from the 30s startup rule: use `--human --notify` with at least 300s and never read secret spans.

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
pairmux wait <name>[,<name>...] [--idle MS] [--pattern RE] [--done] [--note] [--human] [--notify] [--timeout 300s]
```
Several terminals may be named, separated by spaces or commas. The wait then ends on the **first**
of them to satisfy a condition, and `terminal` in the reply says which. This is the join for
driving a fleet: one blocking call over five sub-agents instead of five processes each polling its
own deadline, none of which can wake the others.
- `--idle MS` — after `MS` ms of output silence, refresh terminal state and resolve only when it is
  `idle`, `awaiting-input`, or `dead` (default condition, 800ms). A quiet running command keeps waiting.
- `--pattern RE` — resolve when **new** output (produced after the wait starts) matches the RE2 regex.
  It does **not** scan output already printed before the wait — see troubleshooting.
- `--done` — resolve with `status:"done"` and the `exit_code` when the running command finishes, or
  when the **next** one does if the terminal is idle. Shell terminals only (`E_BAD_ARGS` on a `--cmd`
  terminal, which emits no completion marks). This is how you subscribe to a terminal another agent
  is driving — you do not have to be the one who started the command.
- `--note` — resolve on the next note (or an unseen note already waiting); unrelated completion or
  idle does not satisfy it. Use this for an exact turn boundary in a long-lived program. Its
  hook/wrapper must note the terminal on the **owning endpoint supplied by its creator**; the pane's
  `PAIRMUX_SOCKET` targets a child layer. See [the concrete recipe](collaboration.md#notes-from-inside-a-pane-owning-socket-recipe).
- `--human` — composite handoff: note → `human-done`; answered prompt and resumed execution →
  `running`; shell command completion → `done` plus `exit_code`. A rejected answer that re-prompts
  stays blocked. An unseen pending completion can return even if it happened before this wait,
  unlike `--done` at idle. Read-only waits do not consume it: another human wait may return it
  again until the next `run` settles the pending completion. `send` acknowledges notes but does not
  settle that completion. After `running`, shell `wait <name> --done --idle 800` is an option only
  when the whole command span is known safe: it may replay earlier human input. Otherwise use `ls`
  or a non-sensitive note. Programs use `wait <name> --idle 800`, assessing returned output.
  `human-done` is only a note: do not blindly start pure `--done` if the shell may already be idle.
- `--notify` — best-effort desktop notification to summon a human.
- `--timeout` — overall deadline (default `300s`).

With no condition flag, idle is armed by default. `--pattern` alone waits for a future pattern.
A prompt does **not** end a `--pattern`- or `--done`-only wait, and never ends a `--human` wait —
`awaiting-input` is the reason you handed off, not an outcome. Combine `--pattern`, `--done`,
`--note`, `--human`, and an explicit `--idle` to race requested conditions; the first one satisfied wins. A
pane that dies while any wait is in flight ends it as `dead`. **A `timeout` is not a failure**: the
`next` repeats that exact wait — same condition flags — with double the deadline, and never under
`300s` for a handoff. For ordinary startup, that longer hint is not automatic escalation: inspect
returned output/status or a safe `peek`, and choose the next bounded observation based on the expected
first meaningful signal. Verify healthy startup before unrelated work or a long wait; silence and
`running` alone are insufficient. Long subscriptions (for example 600s) remain appropriate after
verified startup or for intentional passive listening, not as a substitute for a startup check.

A pane dying **during** a wait returns `dead`; an already-dead target fails with `E_DEAD` when arming.

**Handoff privacy is outcome-specific.** `--human` suppresses the terminal span for resumed/completed
handoffs. `human-done` can contain note text in `output`; `notes` can carry messages, and `dead` or
other explicitly raced conditions can return terminal output. This is not redaction and not a promise
of journal secrecy: echoed secrets can persist. Never put credentials in notes; do not automatically
`peek`/`log` the sensitive span just because a `next` hint suggests it. See
[collaboration.md](collaboration.md#privacy-boundary-not-redaction).

For human handoffs, keep the default 300s or a single valid duration of at least 300s; they are excluded
from the 30s startup rule. If the shell/tool client interrupts, immediately repeat the same handoff wait.
On timeout, repeat the same conditions with the longer deadline in `next`, not an action that types the
secret or reads its span.

Read-only: records nothing, takes no writer lock; concurrent observers can wait on the same terminal.
```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","next":["pairmux peek build","pairmux run build \"...\""]}
```
```json
{"schema":"pairmux.v1","ok":true,"status":"human-done","terminal":"dev","mode":"hooks","output":"the token is fixed, go ahead","next":["pairmux peek dev"]}
```
The handoff ended without a note — the human answered in the pane and the migration is running again:
```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"dbmigrate","mode":"hooks","next":["pairmux wait dbmigrate --done","pairmux peek dbmigrate"]}
```
Subscribed with `--done` to a command another agent started:
```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"output":"…\nBUILD SUCCESSFUL","next":["pairmux peek build"]}
```
```json
{"schema":"pairmux.v1","ok":true,"status":"timeout","terminal":"api","mode":"hooks","next":["pairmux peek api","pairmux wait api --pattern ready --timeout 16s"]}
```
A handoff nobody has answered yet — wait again, longer; do not act instead:
```json
{"schema":"pairmux.v1","ok":true,"status":"timeout","terminal":"dbmigrate","mode":"hooks","next":["the human has not answered yet — do NOT type the secret","pairmux wait dbmigrate --human --notify --timeout 10m0s","pairmux peek dbmigrate"]}
```

### peek — recent output + status, no blocking, no lock
```text
pairmux peek <name> [--screen | --tail N]
```
- default: reads at most the final 64 KiB of raw journal, then returns the shaped tail (`--tail N`
  sets the line count, default 60). If an earlier byte prefix was skipped, `omitted_bytes` reports
  its size and `get_full` is the executable `pairmux log NAME --range 1:end` recovery command.
- `--screen`: a live render of the current viewport (tmux `capture-pane -p`, or the built-in virtual
  screen with the same semantics) — useful for full-screen TUIs.

Safe to call any number of times from any number of agents. Surfaces `notes`.
```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","output":"…\n300\n\nuser@host % ","truncated":{"omitted_lines":253,"omitted_bytes":65536,"get_full":"pairmux log build --range 1:end"},"next":["pairmux run build \"echo hello\""]}
```

### send — deliver raw input to a running program
```text
pairmux send <name> [--text S] [--key K ...] [--enter]
```
Applied in order: text, then keys, then a trailing Enter. Does **not** take the write lock, so it can
answer a program a prior `run` is still blocked on. It does take a short `send` lock, so two agents
delivering input to one pane cannot interleave their keystrokes into a line that is neither's; a
holder that has not finished within 2s yields `E_BUSY`. No lock reaches a **human** at the keyboard,
so when one is attached and looking at that window the reply's `next` says you are sharing its
input line.
- `--text S` — literal text (`send-keys -l` on tmux; no shell expansion, no key interpretation).
- `--key K` — a named key, repeatable. Valid: `Enter Escape Tab Space Up Down Left Right Home End
  PPage NPage BSpace DC`, `F1`–`F12`, `C-a`..`C-z`, `M-a`..`M-z`. A plain letter like `--key q` is
  **rejected** — use `--text q` to type the character.
- `--enter` — append a final Enter. At least one of `--text`/`--key`/`--enter` is required.
```json
{"schema":"pairmux.v1","ok":true,"status":"sent","terminal":"deploy","mode":"hooks","next":["a command is running; sent input goes to it","pairmux peek deploy"]}
```

### log — full or filtered history from the journal
```text
pairmux log <name> [--cmd N | --grep RE | --range A:B|A:end]
```
The four modes are mutually exclusive. Resolves truncation and scrolled-off output without re-running.
- default: reads at most the final 4 MiB of raw journal, then returns its last 500 shaped lines.
  A skipped prefix is reported with `omitted_bytes` and an executable `get_full` command.
- `--cmd N` — the complete output of recorded command number `N`.
- `--grep RE` — every RE2-matching line in the complete journal, prefixed with its line number.
- `--range A:B` / `--range A:end` — a complete 1-based inclusive shaped-line range; `end` selects
  through the final line.

Explicit selectors can return large results. Prefer a narrow regex or bounded line range when the
journal is large.
```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminal":"build","mode":"hooks","output":"6:hello world"}
```

### ls — list terminals + status
```json
{"schema":"pairmux.v1","ok":true,"status":"ok","terminals":[{"name":"build","status":"idle","mode":"hooks","last_activity":"2026-07-18T15:13:38Z"}]}
```
Text form is a table; a lock holder pid, pending command, and `[notes:N]` / `[layer:SOCKET]` badges
show inline. `[layer:…]` means that terminal is itself driving a nested pairmux layer — an agent
running agents. Reach it with `pairmux --socket SOCKET ls`, which the reply's `next` spells out.

### kill — end terminal(s); journals are kept
```text
pairmux kill <name> | --all
```
Killing a terminal also tears down any nested layer it was driving, deepest first, and says which
endpoints went with it. A sub-agent left running would keep working, unlisted at every layer anyone
is likely to look at.
```json
{"schema":"pairmux.v1","ok":true,"status":"killed","terminal":"deploy","next":["journal retained under the pairmux state root","pairmux prune deploy reclaims its disk when the history is no longer needed","pairmux ls"]}
```

### prune — reclaim retained journals
```text
pairmux prune [name] [--older-than 7d] [--dry-run]
```
- Removes **dead** terminals' state directories and `.prev` rotation archives; never a live
  terminal's journal (a named prune on a live terminal reclaims only its archive, or returns
  `E_BUSY` when there is none). Pruned history is unrecoverable — read what you need first.
- `--older-than` filters by last activity (Go durations plus `7d` day shorthand); `--dry-run`
  lists without removing; a directory whose write lock is held is kept and reported.

---

## Human and maintenance commands

- `pairmux attach [name]` — put the human in the live pane; needs interactive stdin/stdout. tmux
  backend: a tmux client focused on `name`, refused inside tmux. Built-in backend: attaches their
  terminal straight to the pane (works inside tmux, not on its own pane; no name → the only live
  terminal, several → `E_BAD_ARGS`). Detach with `Ctrl-b d` on both.
- `pairmux watch [--interval 2s]` — a self-refreshing dashboard until `Ctrl-C`. `!!` flags
  awaiting-input, `xx` flags dead.
- `pairmux note <name> <text...>` — record a message for the agent; surfaces in the agent's next
  `run`/`peek`/`wait` `notes`, and resolves `wait --human`.
- `pairmux doctor` — first line `backend`: which backend, why, and the built-in socket path or tmux
  version. Then state-dir writability + retained journal size, per-shell completion tier (probed on a
  throwaway endpoint of that backend), notifier, and (when set) `PAIRMUX_SECRET_PROMPT_RE` validity.
- `pairmux update` — **v0.6.0+; explicit user authorization required.** Update only the currently
  running, verified persistent uv installation from public PyPI to the latest compatible stable
  wheel, without downgrading. Resets saved pins, constraints, extra requirements, and source settings;
  reports `updated`, or `refreshed` after reinstalling the same version. Supports global `--json`,
  no command-specific arguments. Requires installed uv, does not bootstrap it, and does not touch
  tmux or other package managers. Unverified/non-uv/uvx/dev installations fail with `E_UPDATE`;
  follow the hint rather than force-overwriting. Versions through v0.5.3 first need
  `uv tool install --upgrade pairmux` (which follows normal uv configuration). On Windows it always
  refuses with `E_UPDATE`; the hint is to close pairmux and run `uv tool install --upgrade pairmux`.
- `pairmux version` — print the build version.
- `pairmux skill install [--target T|all] [--dry-run]` (`-t` aliases `--target`) — offline installation
  of the five canonical files embedded at build time (SKILL.md + four references). Reports embedded CLI version and bundle
  SHA-256; it is not a remote-latest fetch. Always replaces managed same-name files, including local
  edits, and preserves extra files. To get latest content, obtain a fresh executable first, then
  install again and restart/reload the agent. `all` skips missing configuration roots; `cursor` is
  relative to the current working directory. `codex`/`agents` write `.agents/skills/`, not the legacy
  `.codex/skills/` tree. Rebuilding from refreshed source produces a dev build; `update` refuses dev
  builds under existing policy. See the canonical repo's `install-map.md` for all target paths.

## Completion-detection modes

| mode | how completion is detected |
|------|----------------------------|
| `hooks` | OSC 133 integration from pairmux's bash/zsh shims, the PowerShell prompt wrapper, or native Fish 4+ marks |
| `sentinel` | injected marker carrying `$?` (POSIX-like shells), `$status` (Fish fallback), or `ERRORLEVEL` (`cmd.exe`) |

`hooks` is chosen automatically for supported interactive shells. Fish versions/configurations that
emit no native marks degrade to a Fish-safe sentinel; unknown shells use a shell-compatible sentinel.
These are **shell completion modes**, not terminal kinds. A `--cmd` program also reports `sentinel`
but gets no injected per-command marker and supports neither `run` nor `wait --done`; observe it with
`peek`, `--idle`, future `--pattern`, or `--note`. `doctor` reports finer probe tiers `hooks`,
`hooks-no-C` (notably bash 3.2 and PowerShell), `hooks-degraded->sentinel`, and `sentinel` as diagnostics,
not additional envelope `mode` values.
