---
name: pairmux
description: >-
  Drive long-running, interactive, or background terminal programs reliably from your shell tool.
  Use pairmux whenever a command is slow (builds, tests, installs, migrations), interactive (a REPL,
  a TUI, a pager, a [y/N] confirmation, or a password prompt), or long-lived (a dev server, a watch
  task, tail -f logs) — or whenever a human may need to watch, attach, or take over the same live
  terminal session. pairmux is a thin layer over tmux that gives a blocking CLI which returns exactly
  when a command finishes (so you never sleep-and-guess timing), clean full output history with exit
  codes, and human handoff for secrets. Keywords: tmux, terminal session, background process, dev
  server, watch logs, REPL, interactive CLI, TUI, pager, password prompt, human takeover, long-running.
---

# pairmux — reliable terminals for agents

`pairmux` runs commands inside real tmux panes and gives you a **blocking** CLI: the call returns the
moment the command finishes, goes quiet, or needs input. You never guess how long to wait, and a human
can watch or step into the exact same terminal. Add `--json` to any command for a machine-readable
`pairmux.v1` envelope (examples below are the real JSON).

## When to use pairmux (and when not to)

Use pairmux when timing, interactivity, longevity, or a human matters:

- **Slow** commands — builds, test suites, `npm install`, migrations. Blocks until done; no `sleep`.
- **Interactive** programs — REPLs, TUIs, pagers, `[y/N]` prompts, password prompts.
- **Long-lived** processes — dev servers, `watch`, `tail -f`. Start it, then observe read-only.
- **Human-in-the-loop** — a secret you must not guess, or a human who wants to take over a pane.
- **Shared observation** — several agents reading one terminal's log at once (reads are lock-free).

**Do NOT use pairmux for one-shot short commands.** `ls`, `cat`, `git status`, `mkdir`, a quick
`grep` — just use your normal shell tool. pairmux earns its keep only when a command is slow,
interactive, long-lived, or shared with a human.

## The golden loop

```
1. new    → open a terminal once per workstream:   pairmux new --name build
2. run    → send a command; it BLOCKS until done or --timeout:
              pairmux run build "make -j4" --timeout 30s
3. read `status` and act:
     done            → read output + exit_code, move on
     running         → not finished — pairmux wait build --idle 800   (NEVER sleep)
     awaiting-input  → it wants input — pairmux send build --text y --enter
                       (secret prompt? do NOT guess — hand off, see rules)
4. truncated?        → pairmux log build --cmd N | --grep RE   (read the journal, don't re-run)
5. Always run whatever the envelope's `next` field tells you.
```

## Iron rules

1. **Never sleep-and-guess.** `run` and `wait` block for you. A timeout is **not** a failure — it
   returns `status:"running"` (or `"timeout"`) with a `next` telling you how to keep waiting.
2. **One command per terminal at a time.** A second `run` while one is still going returns
   `E_BUSY` ("a command is still running"). Open another terminal with `new` for parallel work.
3. **Answer a prompt once.** Send the answer a single time; do not spam Enter.
4. **Never type or guess a secret.** On a password/passphrase/passcode prompt, pairmux says
   `do NOT guess or type secrets`. Hand off to a human: `pairmux wait <name> --human --notify`.
5. **Prefer reading the log over re-running.** The journal already has the full output —
   `pairmux log` is free and instant; re-running wastes time and can change state.
6. **`next` is always your next command. `notes` are messages from a human — read and obey them.**

## Reading the envelope

Two fields are always present: `schema` (`"pairmux.v1"`) and `ok`. Everything else appears only when
relevant. The three you act on most:

- **`status`** — what state things are in (table below).
- **`next`** — an array of concrete commands; the top one is your next step.
- **`notes`** — unseen messages a human left via `pairmux note`. If present, **read them and follow them.**

Terminal statuses (from `run`, `peek`, `wait`, `ls`):

| status | meaning | your move |
|--------|---------|-----------|
| `done` | command finished (carries `exit_code`, `duration_ms`) | read `output`, continue |
| `running` | still executing (or `run` hit its timeout) | `wait` — never `sleep` |
| `awaiting-input` | quiet, last line looks like a prompt | `send` the answer (or hand off if secret) |
| `idle` | shell at a prompt, nothing running | send the next command |
| `dead` | the pane is gone; journal is kept | `new` a fresh terminal |

`run` also reports `done`/`running`; `wait` reports `idle`/`pattern-found`/`human-done`/`timeout`.
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
pairmux wait build --idle 800     # returns when output has been quiet for 800ms
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
| `pairmux new [--name N] [--cwd D] [--cmd "..."]` | open a terminal (`--cmd` launches a program you drive with `send`) |
| `pairmux run <name> "<cmd>" [--timeout 60s] [--head 50] [--tail 200]` | run a command, block until done/timeout |
| `pairmux wait <name> [--idle MS \| --pattern RE \| --human] [--notify] [--timeout 300s]` | block until a condition |
| `pairmux peek <name> [--screen \| --tail N]` | read recent output + status, no blocking, no lock |
| `pairmux send <name> [--text S] [--key K ...] [--enter]` | send input to a running program |
| `pairmux log <name> [--cmd N \| --grep RE \| --range A:B]` | read full/filtered history from the journal |
| `pairmux ls` | list terminals + status | 
| `pairmux kill <name> \| --all` | kill terminal(s); journals are kept |
| `pairmux note <name> "<text>"` · `attach [name]` · `watch` | human side-channel / take-over |

## Go deeper (references/)

- **[references/commands.md](references/commands.md)** — every command, every flag, the full `pairmux.v1` envelope schema, and all error codes.
- **[references/interactive.md](references/interactive.md)** — REPLs, pagers, `[y/N]` confirmations, `send` text-vs-keys, and the never-guess-secrets rule.
- **[references/collaboration.md](references/collaboration.md)** — `attach`/`watch`/`note` and the human handoff loop (`wait --human --notify`).
- **[references/troubleshooting.md](references/troubleshooting.md)** — `E_BUSY`, dead terminals, `wait --pattern` gotchas, huge journals, sentinel mode.
