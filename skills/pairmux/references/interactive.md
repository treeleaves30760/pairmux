# Interactive programs playbook

Some programs stop and wait for input: a `[y/N]` confirmation, a REPL, a pager, a password prompt.
pairmux detects when a terminal is waiting and refines its status from `running` to `awaiting-input`,
with one hard rule for secrets.

## awaiting-input

When a command goes quiet and its last screen line looks like a prompt, `status` becomes
`awaiting-input` and `next` shows how to answer:

```bash
pairmux run deploy "terraform apply" --timeout 30s
```
```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"deploy","mode":"hooks","output":"\nDo you want to continue? [Y/n] ","next":["pairmux send deploy --text <answer> --enter"]}
```
```bash
pairmux send deploy --text yes --enter
```

pairmux decides this mostly from the **terminal itself**, not from the words on it:

- **Echo off while canonical (whole-line) input remains enabled is a credential signal** on
  macOS/Linux; it is independent of wording or language. Raw-mode TUIs also turn echo off, so
  echo-off alone is not a secret verdict. Windows has no tty line discipline to inspect and relies
  on wording instead.
- **A recognized phrasing** — `[y/N]`, `(yes/no)`, `password:`, `PIN:`, OTP/MFA codes, API keys,
  pagers (`--More--`, `(END)`, a bare `:`), "press any key" — classifies without waiting.
- **Anything else that goes quiet mid-line** for ten seconds can be reported as unrecognized.
  `Building... ` can look like a question even while work continues: inspect before answering.
- **Raw-mode programs** can also produce inferred `awaiting-input`. Read `peek --screen`: a TUI
  redraw is not a line-oriented prompt, and inference is **not proof** that its turn is done or
  input is safe. Wait for a future named event or an explicit note when a precise boundary matters.

It **never auto-answers** — it reports an observation, and you decide. A quiet credential command
can remain `running` when detection misses its prompt; hand off anyway rather than guessing.

`PAIRMUX_SECRET_PROMPT_RE` still extends the phrase list for a prompt that echoes what you type and
is nonetheless sensitive; `pairmux doctor` validates it. On macOS/Linux it is rarely needed: a
prompt that hides your typing is already classified without it. On Windows it is the only way to
teach pairmux a differently worded credential prompt.

## The never-guess-secrets rule

When canonical whole-line input hides what gets typed, or the wording is secret-shaped — a
**password, passphrase, passcode, PIN, one-time or verification code, API key, or token** — pairmux
classifies it as a secret and refuses to offer an answer. It points at a human handoff instead:

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"dbmigrate","mode":"hooks","output":"\nPassword: ","next":["do NOT guess or type secrets","pairmux wait dbmigrate --human --notify   # hand off to the human"]}
```

**Do not invent a password, and do not pull one out of earlier output or the environment.** Hand off:

```bash
pairmux wait dbmigrate --human --notify
```

This blocks and notifies the human. A note returns `human-done` (not command completion); resumed
execution returns `running`; a finished shell command can return `done` + `exit_code`. Ordinary shell
`--done --idle 800` may replay earlier human input: use it only when the whole command span is known
safe, not just later output. Otherwise use status-only `ls` or a non-sensitive human note. A program
uses `--idle 800`, never `--done`; assess any output returned. After a note, check status (`ls`)
before deciding: pure `--done` on an already-idle shell waits for a future command. Re-prompts stay
blocked. On timeout repeat the longer handoff wait; if
the tool client interrupts, immediately reissue the same wait. Keep the 300s default or one valid
explicit duration of at least 300s.

Resumed/completed handoffs suppress the terminal span, but `human-done` can return note text and
other outcomes can carry output. This is not guaranteed redaction or journal secrecy. Never put
credentials in notes or automatically `peek`/`log` the sensitive span. See
[collaboration.md](collaboration.md) for outcome handling and privacy limits.

## send: text vs keys

`send` applies its parts in order — text, then keys, then a trailing Enter. Literal means no
expansion by pairmux; the receiving shell/REPL still interprets the text you submit.

- `--text S` sends **literal** text (via `send-keys -l` on tmux), so `$HOME`, `;`, and quotes are
  not interpreted.
- `--key K` sends a named key. Repeatable. Valid keys: `Enter Escape Tab Space Up Down Left Right
  Home End PPage NPage BSpace DC`, `F1`–`F12`, `C-a`..`C-z`, `M-a`..`M-z`.
- `--enter` appends a final Enter.

```bash
pairmux send repl --text "print('hi')" --enter    # type a line and run it
pairmux send app  --key C-c                        # interrupt (Ctrl-C)
pairmux send menu --key Down --key Down --key Enter # navigate a TUI
```

**Common mistake:** passing a plain character as a key. `--key q` is rejected — use `--text q` to
type the letter, or a named key like `--key Enter`. The error's `hint` says exactly this.

## Answer a prompt exactly once

After a `send`, do **not** re-send the same answer or spam Enter. For a shell command, use
`wait <name> --done --idle 800 --timeout 30s` to race completion against a later prompt; for a program,
use `wait <name> --idle 800 --timeout 30s` or its explicit future event/note. Use a shorter deadline for
fast signals, inspect the returned evidence, and use `peek` only if needed and safe.
`status:"sent"` means delivery, not success, completion, or permission to send again.

```json
{"schema":"pairmux.v1","ok":true,"status":"sent","terminal":"deploy","mode":"hooks","next":["a command is running; sent input goes to it","pairmux peek deploy"]}
```

## Pagers

A pager (`git log`, `less`, `man`) shows as `awaiting-input` with a `--More--`/`(END)`/`:` last line.
Quit it by sending `q` as text:

```bash
pairmux send review --text q     # NOT --key q (that is rejected)
```

To avoid pagers entirely, disable them at the source when you run the command:
`git --no-pager log`, `PAGER=cat`, `GIT_PAGER=cat`.

## REPLs (Python, node, psql, …)

A REPL never "finishes" until you exit it. When you know the entrypoint up front, use a program
terminal and drive it with `send` + `peek`:

```bash
pairmux new --name repl --cmd "python3" # running (or dead if it already exited)
pairmux peek repl                     # inspect startup/readiness already printed
pairmux send repl --text "2 + 2" --enter
pairmux wait repl --idle 800 --timeout 5s # fast prompt/death observation, not shell completion
pairmux peek repl                     # verify "4" and the next prompt
pairmux send repl --text "exit()" --enter
```

A `--cmd` terminal is a program, even with `mode:"sentinel"`: `run` and `wait --done` return
`E_BAD_ARGS`. Its exit is `dead`. Idle waits can surface inferred prompts, not guaranteed REPL-turn
completion; use an explicit owning-socket note for a reliable protocol boundary (see collaboration).
Pattern waits match only future output: inspect startup readiness before waiting for a later event.

If the REPL needs setup performed by an existing shell, start a shell terminal first and use `run`.
A recognized quiet prompt returns `awaiting-input`, while an unrecognized REPL remains `running`:

```bash
pairmux new --name repl
pairmux run repl "source .venv/bin/activate && python3" --timeout 30s
```

## Full-screen TUIs

For a full-screen app (vim, htop, a menu), the journal tail is noisy — use the screen render instead:

```bash
pairmux peek tui --screen     # a snapshot of the current viewport (capture-pane semantics)
pairmux send tui --key Down --key Enter
pairmux send tui --key Escape
```
