---
name: pairmux
description: >-
  Drive interactive terminal programs your exec-style shell tool cannot: REPLs, TUIs, pagers,
  ssh/sudo password prompts, docker exec -it, git rebase -i — real PTYs with persistent shell state
  (venv, exports, nvm) that survives between commands. When a prompt needs a credential or a
  judgment call, hand off live to a human who can watch, attach, take over the same terminal, and
  hand back. Also covers long-lived processes (dev servers, watch tasks, tail -f logs) and slow
  commands that must share that same live terminal — with blocking calls that return on requested
  outcomes instead of sleep-and-guess timing, and clean full output history with exit codes.
  Keywords: tmux, terminal session, PTY, interactive CLI, REPL, TUI, pager, password prompt, human
  takeover, human-in-the-loop, persistent shell, dev server, watch logs, background process,
  long-running.
---

# pairmux — reliable terminals for agents

`pairmux` runs commands inside real tmux panes — actual PTYs with persistent shell state — and gives
you a **blocking** CLI: `run` returns on completion, input, or timeout; `wait` returns when the
condition you requested is met, the pane dies, or its timeout expires. Interactive programs become
drivable, a human can watch or step into the same terminal at any moment, and you never guess how
long to wait. Add `--json` to any command for a machine-readable `pairmux.v1` envelope.

## When to use pairmux (and when not to)

Use pairmux when interactivity, a persistent live shell, a human, or longevity matters:

- **Interactive** programs — REPLs, TUIs, pagers, `[y/N]` prompts, password prompts, `ssh`,
  `git rebase -i`, `docker exec -it`, `npm init`. An exec-style shell tool has no PTY, so these
  are impossible there, not merely awkward. This is pairmux's core job.
- **Human-in-the-loop** — a secret you must not guess, a judgment call mid-command, or a human who
  wants to watch the pane live, take it over, and hand it back.
- **Persistent shell state** — `source venv/bin/activate`, `conda activate`, `export`, `nvm use`
  done once in a live shell, instead of re-composed into every command.
- **Long-lived** processes — dev servers, `watch`, `tail -f`. Start it, then observe read-only.
- **Shared observation** — several agents reading one terminal's log at once (reads are lock-free),
  or a human watching your work as it happens.
- **Slow commands in that same live terminal** — a build or migration that needs the venv, the ssh
  session, or a watching human. `run` blocks until done; no `sleep`.

**Do NOT use pairmux for one-shot short commands.** `ls`, `cat`, `git status`, `mkdir`, a quick
`grep` — just use your normal shell tool. And a long but **non-interactive** command that needs no
live shell state and no human (a plain `make`, a test suite in a fresh env) is often served just as
well by your harness's own background execution — reach for pairmux when the command is
interactive, shares a terminal or its state, or may need a human to step in.

## The golden loop

```
1. new    → open a terminal once per workstream:   pairmux new --name build
2. run    → send a command; it BLOCKS until done or --timeout:
              pairmux run build "make -j4" --timeout 30s
3. read `status` and act:
     done            → read output + exit_code, move on
     running         → not finished — pairmux wait build --idle 800   (NEVER sleep)
     known hung      → pairmux send build --key C-c; pairmux wait build --idle 800
                       then `run` the recovery command in that same terminal
     awaiting-input  → it wants input — pairmux send build --text y --enter
                       (secret prompt? do NOT guess — hand off, see rules)
4. truncated?        → pairmux log build --cmd N | --grep RE   (read the journal, don't re-run)
5. Read `next` in order. Obey safety/information entries, then run the first applicable command.
```

## Iron rules

1. **Never sleep-and-guess.** `run` and `wait` block for you. A timeout is **not** a failure — it
   returns `status:"running"` (or `"timeout"`) with a `next` telling you how to keep waiting.
2. **One command per terminal at a time.** A second `run` while one is still going returns
   `E_BUSY` ("a command is still running"). Open another terminal with `new` for parallel work.
3. **Answer a prompt once.** Send the answer a single time; do not spam Enter.
4. **Never type or guess a secret.** On a secret-shaped prompt (password/passphrase/passcode, PIN,
   OTP/MFA/verification codes, API keys, localized sudo prompts), pairmux says
   `do NOT guess or type secrets`. Hand off to a human: `pairmux wait <name> --human --notify`.
   That wait ends on a human `note` **or** on the human being finished — the prompt is answered and
   the terminal is moving again (`running`; `wait --done` follows the rest), or the command
   finished outright (`done` + `exit_code`). Treat either as "the human is done", and note that
   `--human` returns no `output`, so the secret is never quoted back to you. A `timeout` means the
   human has not come yet: **wait again** with the longer deadline in `next`, never act instead. If
   the shell/tool client interrupts before pairmux returns, immediately reissue that same wait;
   never shorten pairmux's 300s default (one valid explicit timeout of at least 300s is
   equivalent).
   This is read from the terminal, not the wording: a prompt that hides your typing is a secret
   whatever language it is in and whichever tool asked. A prompt marked as unrecognized instead
   (`quiet mid-line, but no prompt was recognised`) is a guess — `peek --screen` before answering,
   and `wait --done` if it turns out to still be working.
5. **Subscribe, don't poll.** To follow a terminal another agent (or a human) is driving,
   `pairmux wait <name> --done` blocks until its command finishes and reports the `exit_code`.
   Name several terminals (`wait a,b,c`) to block on all of them and return on the first to fire.
   For a terminal holding a long-lived program — another agent's UI — there is no completion mark:
   have the pane signal with `pairmux note "$PAIRMUX_NAME" "..."` and block on `wait <name> --note`.
   `wait` takes no lock, so any number of agents can hold one on the same terminal and all of them
   wake on the same completion.
6. **Prefer reading the log over re-running.** The journal already has the full output —
   `pairmux log` is free and instant; re-running wastes time and can change state.
7. **Treat `next` as contextual hints, not a script.** Read entries in order and obey safety/prose.
   Replace placeholders with real values; never execute prose or placeholder text literally. Run the
   first applicable command. Final replies may omit `next`. Read and obey human `notes`.
8. **Prefer program terminals for known interactive entrypoints.** Start a REPL, TUI, or persistent
   server with `pairmux new --name <name> --cmd "<program>"`; then drive that live program with
   `send`/`peek`. Use `run` when the command needs an existing shell.
9. **Recover hung commands in place.** When a task requires the same terminal/session, use
   `pairmux send <name> --key C-c`, then `pairmux wait <name> --idle 800`, then `run` the recovery
   command on that name. `kill` destroys the terminal; use it only as a last resort when a fresh
   terminal is explicitly acceptable.
10. **Pattern waits observe future output only.** If readiness text may have appeared during `run` or
   `new --cmd`, read that returned output or use `peek`/`log --grep`; never wait for a past line.
   Use `wait --pattern` only before an event you still expect to happen.

## Reading the envelope

Two fields are always present: `schema` (`"pairmux.v1"`) and `ok`. Everything else appears only when
relevant. The three you act on most:

- **`status`** — what state things are in (table below).
- **`next`** — optional ordered hints: safety/prose, placeholders, then applicable commands.
- **`notes`** — unseen messages a human left via `pairmux note`. If present, **read them and follow them.**

Terminal statuses (from `run`, `peek`, `wait`, `ls`):

| status | meaning | your move |
|--------|---------|-----------|
| `done` | command finished (carries `exit_code`, `duration_ms`) | read `output`, continue |
| `running` | still executing (or `run` hit its timeout) | `wait` — never `sleep` |
| `awaiting-input` | quiet, last line looks like a prompt | `send` the answer (or hand off if secret) |
| `idle` | shell at a prompt, nothing running | send the next command |
| `dead` | the pane is gone; journal is kept | `new` a fresh terminal |

`run` also reports `done`/`running`. `wait` reports `idle`/`awaiting-input` for an idle wait,
`done` (with `exit_code`) for `--done`, `running` when a `--human` handoff is answered and the
terminal resumes, `pattern-found`, `human-done`, `dead`, or `timeout`, depending on the requested
condition. A `timeout` carries a `next` that repeats the same wait with a longer deadline — follow
it rather than giving up or switching strategy.
Errors set `ok:false` with a stable `error.code`: `E_NO_TERMINAL`, `E_EXISTS`, `E_BUSY`, `E_DEAD`,
`E_BAD_ARGS`, `E_TMUX`, `E_INTERNAL`. The error's `hint`/`next` tells you how to recover.

## Worked examples (real envelopes)

Fast command finishes inside `run`:

```bash
pairmux new --name build
pairmux run build "echo hello world"
```
```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"hello world"}
```

Slow command outlives its timeout — keep waiting, do not sleep:

```bash
pairmux run build "make -j4" --timeout 5s
```
```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"build","mode":"hooks","output":"\ncompiling…","next":["pairmux peek build","pairmux log build --cmd 1"]}
```
```bash
pairmux wait build --idle 800     # returns when the shell is truly idle, not merely quiet
```

A `[y/N]` prompt surfaces as `awaiting-input`; answer it once:

```bash
pairmux run deploy "terraform apply"
```
```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"deploy","mode":"hooks","output":"\nDo you want to continue? [Y/n] ","next":["pairmux send deploy --text <answer> --enter"]}
```
```bash
pairmux send deploy --text yes --enter
```

A **secret** prompt — never guess, hand off to a human:

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"dbmigrate","mode":"hooks","output":"\nPassword: ","next":["do NOT guess or type secrets","pairmux wait dbmigrate --human --notify   # hand off to the human"]}
```

Truncated output — get the rest from the journal, never re-run:

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"output":"1\n2\n…\n300","truncated":{"omitted_lines":50,"get_full":"pairmux log build --cmd 3"}}
```
```bash
pairmux log build --grep "error|FAIL"     # find the needle without re-running
```

A human left a note — it rides along in `notes`; obey it:

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"output":"resumed","notes":["use the staging token, not prod"]}
```

## Command cheat-sheet

| command | purpose |
|---------|---------|
| `pairmux new [--name N] [--cwd D] [--cmd "..."]` | open a terminal (`--cmd` is preferred for a known REPL/TUI/server) |
| `pairmux run <name> "<cmd>" [--timeout 60s] [--head 50] [--tail 200]` | run a command, block until done/timeout |
| `pairmux wait <name> [--idle MS] [--pattern RE] [--human] [--notify] [--timeout 300s]` | block until a requested condition |
| `pairmux peek <name> [--screen \| --tail N]` | read recent output + status, no blocking, no lock |
| `pairmux send <name> [--text S] [--key K ...] [--enter]` | send input to a running program |
| `pairmux log <name> [--cmd N \| --grep RE \| --range A:B\|A:end]` | read full/filtered history from the journal |
| `pairmux ls` | list terminals + status | 
| `pairmux kill <name> \| --all` | kill terminal(s); journals are kept |
| `pairmux prune [name] [--older-than 7d] [--dry-run]` | reclaim dead terminals' retained journals |
| `pairmux note <name> "<text>"` · `attach [name]` · `watch` | human side-channel / take-over |

## Go deeper (references/)

- **[references/commands.md](references/commands.md)** — every command, every flag, the full `pairmux.v1` envelope schema, and all error codes.
- **[references/interactive.md](references/interactive.md)** — REPLs, pagers, `[y/N]` confirmations, `send` text-vs-keys, and the never-guess-secrets rule.
- **[references/collaboration.md](references/collaboration.md)** — `attach`/`watch`/`note` and the human handoff loop (`wait --human --notify`).
- **[references/troubleshooting.md](references/troubleshooting.md)** — `E_BUSY`, dead terminals, `wait --pattern` gotchas, huge journals, sentinel mode.
