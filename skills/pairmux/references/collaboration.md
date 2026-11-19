# Human collaboration and subscriptions

pairmux terminals are real PTYs/ConPTYs. A human can watch, take over the same session, answer a
credential prompt or make a judgment call, then hand control back. Notes explain what happened;
resumed execution and shell completion can also end a handoff without a note.

## The human commands

- **`pairmux tui`** (also what a bare `pairmux ls` opens at the human's terminal): the human's
  interactive manager for the whole layer tree — attach (and return), create, kill, delete stale
  terminals, note, send, read output history and activity, run any command with readable output.
  Agents never launch it; to point the human at it, say "run `pairmux ls`".
- **`pairmux watch`**: live dashboard (status, lock holder, command, last activity). `!!` means
  awaiting-input, `xx` dead; `Ctrl-C` quits. The human operates the dashboard.
- **`pairmux attach [name]`**: human takes the live pane. Detach with `Ctrl-b d` (tmux's configured
  binding; fixed on built-in). Built-in `Ctrl-b Ctrl-b` sends a literal `Ctrl-b`. tmux attachment
  refuses to nest inside tmux; built-in attachment works inside tmux, but not inside its own pane.
- **`pairmux note <name> "<text>"`**: human/agent side channel, surfaced in `notes` on the next
  `run`/`peek`/`wait`; resolves `--note` as `note`, or `--human` as `human-done`.

These commands address the layer that **owns** the terminal; from above, a path such as
`worker/tests` reaches a terminal in a lower layer. From inside a pane, report to your owner with
`pairmux report` — see [the recipe below](#reporting-from-inside-a-pane).

## The handoff loop (secret prompt)

**1. Recognize the boundary.** Passwords, passphrases, PINs, OTP/MFA codes, API keys, tokens and
localized credential prompts require a human. Do not guess, extract a secret from the environment,
or copy one from earlier output. Detection is best-effort: a known credential command that goes
quiet at `running` can also need handoff (notably Windows tools that read key by key, where
detection relies on wording).

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"dbmigrate","mode":"hooks","output":"Password: ","next":["do NOT guess or type secrets","pairmux wait dbmigrate --human --notify   # hand off to the human"]}
```

**2. Summon the human and block.**

```bash
pairmux wait dbmigrate --human --notify
```

`--notify` is best-effort desktop notification (macOS `osascript`, Linux `notify-send`, Windows
PowerShell). Keep the 300s default, or one valid explicit duration of at least 300s. If your shell/tool
client interrupts before pairmux returns, immediately reissue the **same** handoff wait; do not
shorten it or type the secret. If pairmux returns `timeout`, repeat its longer handoff wait, with the
same conditions. A deadline does not authorize acting on the human's behalf. **Credential handoffs are
excluded from the 30s startup rule**: never shorten them or inspect secret spans for a startup check.

**3. Give the human the same terminal and endpoint.** They attach, type directly, and optionally leave
a **non-sensitive** note after detaching:

```bash
pairmux attach dbmigrate
pairmux note dbmigrate "authentication complete; continue with staging"
```

Do not write while the human owns the input line. Attachment itself is not a journal event; `send`
can warn about sharing input, but locks cannot stop a human's keystrokes.

**4. Distinguish handoff outcomes before continuing.**

| outcome | what happened | next action |
|---------|---------------|-------------|
| `human-done` | a note arrived (possibly already unseen) | read note text; this is **not command completion** |
| `running` | the prompt was answered and execution resumed | shell completion wait only if the whole command span is known safe; program uses `--idle 800`, assessing returned output |
| `done` + `exit_code` | shell command finished | use the exit code; do not wait for another completion |
| `dead` | pane died | investigate only known-safe output; journal remains |
| `timeout` | no requested outcome before deadline | repeat the longer handoff wait |

A wrong answer that re-prompts keeps the handoff blocked. An unseen pending shell completion can
satisfy `--human` even when it landed before the wait; repeated read-only waits can return it again
until the next `run` settles the pending completion. `send` acknowledges notes but does not settle
that completion. Do not loop on a handoff that already returned a completed result.

A resumed shell handoff can look like this (no terminal span):

```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"dbmigrate","mode":"hooks","next":["pairmux wait dbmigrate --done","pairmux peek dbmigrate"]}
```

**Only if the whole command span is known safe**, follow completion or an intervening prompt/idle:

```bash
pairmux wait dbmigrate --done --idle 800
```

This ordinary wait does not extend handoff privacy. `--done` reads from the pending command's start
to its completion mark: it may replay earlier human input, not just output produced after handoff.
Later output being safe is insufficient; the **whole command span** must be known safe. Otherwise
ask the human to confirm completion with a non-sensitive note, or check state with `pairmux ls`
instead of reading the credential span.

After `human-done`, do not blindly call pure `--done`: at idle it subscribes to the **next** command,
not the one the human may already have finished. Check `ls` (no terminal output) first. If idle,
continue as authorized, without assuming success; if still prompting, coordinate continued human
input; if running, follow the appropriate wait only when the whole returned span is known safe.
`--done --idle 800` is an alternative only when
the whole command span is known safe and either outcome is useful. For a resumed **program** terminal named `repl`, use `pairmux wait repl --idle 800`, never
`--done`. A prompt there is an observation, not a guaranteed program-turn boundary.

## Privacy boundary: not redaction

- Resumed/completed `--human` outcomes suppress the **terminal span** the human typed into.
- `human-done` can include **note text** in `output`; messages can also appear in `notes`.
- `dead` and other explicitly raced outcomes (for example a pattern) can include terminal output.
- This is **not redaction**. It does not guarantee journal secrecy: a program that echoes a secret
  can leave it in the terminal, screen, retained journal, or another reader's output.
- **Never put credentials in notes.** Notes are recorded messages, not a secret channel.
- **Do not automatically `peek`/`log` a sensitive span** after handoff, even if `next` includes a
  peek hint. Use a status-only `ls`, non-sensitive human confirmation, or an explicitly known-safe
  later range. Do not claim that a password was never logged or visible.

## Notes flow both ways

A human or agent can leave task context at any time:

```bash
pairmux note build "use staging; do not deploy to production"
```

Read and follow `notes` within the task's authorization and safety constraints. `ls` shows
`[notes:N]`. Reading does not consume a note; it stays visible until a subsequent `run`/`send`
acknowledges it. `--note` and `--human` also return on an already-unseen note, so note-before-wait ordering
is supported. The returned `note`/`human-done` means a message, not a command result or fresh input
permission. When building a turn protocol, agree on the note's meaning and send the next instruction
only after processing it.

If a handoff note arrived while a credential prompt is still pending, another `--human`/`--note`
wait can immediately replay that same note. Do not spin or send a dummy key just to acknowledge it:
the key would reach the credential prompt. Ask the human to finish and give a non-sensitive
completion confirmation; check status without reading the sensitive span.

## Subscribing without blind polling

When launching work, submit the command first, then estimate time to the first meaningful signal,
not the total runtime. Generally use an initial bounded observation of 30s (shorter for fast tasks),
preferably the bounded `run` itself or an event-aware `wait`. Inspect returned output/status or a safe
`peek`; if `run` already supplied completion or healthy-startup evidence, do not add a redundant wait.
Verify healthy startup before unrelated work or a long wait: look for expected progress/readiness,
not just `running` or silence. Unknown/silent is not healthy; a timeout is not failure, so investigate
missing evidence instead of blindly doubling deadlines. A brief sleep is only a fallback when no
suitable blocking observation is available, followed by a safe check, not repeated blind polling.
Credential handoffs keep `--human --notify` at ≥300s and do not permit reading secret spans.

Long passive subscriptions may use 600s **after verified startup or for intentional passive listening**
(including future work another agent or a human will drive), not instead of checking newly launched work:

```bash
pairmux wait build --done --timeout 600s
pairmux wait a,b,c --done --timeout 600s   # first terminal to complete wins
```

If the shell is idle when armed, `--done` watches a **future completion**, not an old result. It
intentionally stays blocked at prompts. When driving a command and needing prompts/idle too, add
`--idle 800`. Reads/waits take no writer lock and do not record events, so many subscribers can
observe the same completion.

Program terminals (`new --cmd`) reject `--done`: use current output for already-printed readiness,
future `--pattern` for a later event, `--idle` for a prompt/death observation, or an explicit `--note`
for an exact turn boundary. Raw-mode inferred `awaiting-input` is not proof that a turn finished.

## Reporting from inside a pane

Inside terminal `worker`, `PAIRMUX_SOCKET` points to the **child layer** for terminals created *by*
that worker, so a bare `pairmux note worker ...` from inside it looks in the wrong layer. Use
`report`, which writes to the worker's own row in the layer that owns it:

```bash
pairmux report "turn complete"
```

A runnable REPL demonstration calls it from the program itself (no shell interpolation):

```bash
# Creator's shell; Python is a program terminal, not a shell terminal.
pairmux new --name worker --cmd "python3 -q"
pairmux peek worker
pairmux send worker --text "import subprocess; print(2 + 2); subprocess.run(['pairmux', 'report', 'turn complete'], check=True)" --enter
pairmux wait worker --note
```

The calculation emits its note before the wait or while it blocks; both work. `--note` ends on the
signal without mistaking a quiet REPL/TUI for a finished turn. Stop hooks and wrappers inside the
pane should call `pairmux report "..."` the same way; they need no socket or name configuration.
The older explicit form, `pairmux --socket <owner> note <own name> "..."`, still works. A pane can
never note, drive or kill anything else above its own layer (`E_SCOPE`); see
[hierarchy.md](hierarchy.md).
