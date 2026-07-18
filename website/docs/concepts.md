---
title: Concepts
description: How pairmux is built — tmux as a state engine, the journal as source of truth, and reliable completion detection.
---

# Concepts

This page is for engineers evaluating the design. It explains *why* pairmux is built the way it is, not just what it does.

pairmux is deliberately **not** a new terminal. It does not implement a PTY or a VT parser, and it runs **no daemon**. It is a thin coordination layer over tmux that adds exactly the three things agents need and raw tmux lacks: a signal for *when a command is done*, *clean full history*, and a *shared writer discipline* so humans and agents can use the same live terminal.

## Architecture

```mermaid
flowchart LR
  subgraph agents [Agents]
    A1[Agent A<br/>uses its own Bash tool]
    A2[Agent B<br/>read-only observer]
  end
  subgraph cli [pairmux CLI - no daemon]
    RUN[run / send / wait / peek / log]
    LOCK[writer lock<br/>flock per terminal]
    DETECT[completion and status<br/>OSC133 / sentinel / idle refresh]
    SHAPE[output shaping<br/>strip ANSI / collapse CR / truncate]
  end
  subgraph tserver [tmux server]
    P1[(pane build)]
  end
  J[(journal<br/>raw.log + index.jsonl)]
  H((human<br/>attach / watch))

  A1 --> RUN --> LOCK --> P1
  A2 -->|peek / log, lock-free| J
  P1 -->|pipe-pane raw bytes| J
  J --> DETECT --> SHAPE --> A1
  H <-->|native tmux attach| P1
  H -->|pairmux note| J
```

Everything is short-lived: `run`/`wait` are ordinary processes that poll the journal file until their condition is met, then exit. There is no background service to install, supervise, or crash. State lives in two places: tmux pane user-options (which live and die with the pane) and a socket-specific journal namespace under `~/.local/state/pairmux/.sockets/`.

## tmux as the terminal state engine

tmux already maintains screen rendering, scrollback, and — crucially — lets a human `attach`. pairmux leans on all of it instead of reimplementing any of it:

- **No PTY, no VT parser.** pairmux only ever *exec*s tmux subcommands (`new-window`, `send-keys -l`, `pipe-pane`, `capture-pane`, pane options).
- **Human access is nearly free.** `pairmux attach` is a thin wrapper over `tmux attach`; `peek --screen` is a `capture-pane` render. Because the human and the agent share one real tmux pane, a human can literally type into the command the agent is running.

This is the opposite of headless-PTY tools (`ht`, `agent-tty`), which isolate the terminal by design. For pairmux, human-in-the-loop *is* the reason it exists, so sharing a real tmux pane is the point.

## The journal: single source of truth

When a terminal is created, pairmux attaches `tmux pipe-pane` to stream the pane's **raw output bytes** into a journal. `capture-pane` is demoted to an auxiliary "what does the screen look like right now" view.

```text
~/.local/state/pairmux/
  .sockets/<sha256(endpoint identity)>/
    <terminal>/
      raw.log       # pipe-pane raw bytes (mode 0600)
      index.jsonl   # {ts, type, cmd_id, offset, exit_code?, text?} — one event per line
      meta.json     # name, pane id, shell, mode, socket
```

The journal is what makes three otherwise-hard problems easy:

- **Truncation is never silent.** A shaped command reply carries `head` + `tail` plus a
  `truncated.get_full` command. Bounded recent views also report the raw prefix as
  `omitted_bytes` and point to `pairmux log <name> --range 1:end`.
- **Scrolled-off output is still there.** Explicit `pairmux log --grep`/`--range`/`--cmd` requests
  query the complete selected history, not the viewport or only the recent byte window.
- **Multi-agent reads are lock-free.** Reading a file needs no coordination, so any number of agents can watch the same terminal (for example, several agents tailing one dev-server's log).

`run` captures a command's output by recording the journal byte-offset when it sends the command (`cmd_start`) and the offset of the completion mark (`cmd_end`), then shaping the bytes between them.

## The blocking CLI as the "poke"

The endpoint identity includes the canonical `TMUX_TMPDIR`, uid, and tmux socket label. Hashing it keeps
paths bounded and prevents equal terminal names on different tmux servers from sharing metadata.
Conventional default-endpoint journals from older releases remain readable in `<root>/<terminal>/`
without an implicit move. Legacy custom-socket metadata is not claimed because it lacks enough
information to attribute it to a full endpoint safely.

The timing problem — *when should the agent read the output?* — is solved without any push channel. The insight: an agent's shell tool call is **already blocking**. So pairmux makes the CLI block:

> `pairmux run` polls for completion or a quiet prompt. `pairmux wait` polls for the requested idle,
> pattern, or human-note condition. Both return when the pane dies or their timeout fires.

The agent never writes `sleep` and never decides how long to wait. Polling, quiescence detection, and
timeouts all live inside the CLI. A `run` timeout is not a failure: it returns `status: running` with
the current tail and a `next` step. A `wait` timeout returns `status: timeout` and another bounded wait
hint.

## Completion detection and the idle backstop

Knowing a command *finished* (and its exit code) drives everything. Terminals store one of two
completion modes; idle waiting is a separate status backstop:

1. **OSC 133 shell integration (primary).** `new` launches bash with `--rcfile` and zsh through a
   `ZDOTDIR` shim. Fish 4+ emits compatible marks natively, so pairmux launches the selected fish
   binary without replacing the user's configuration. The journal scanner accepts both BEL- and
   ST-terminated OSC 133 `A`/`B`/`C`/`D` marks, including fish's attributes; `D` carries the exit
   code. Terminals using this path report `mode: hooks`.
2. **Sentinel (fallback).** Unknown shells and explicit `--cmd` programs use a nonprinting OSC marker.
   A nominal hooks shell that emits no ready mark also degrades to this mode. POSIX-like shells insert
   the previous status through `$?`; fish uses `$status`. Terminals report `mode: sentinel`.
3. **Quiescence plus state refresh (status backstop).** `wait --idle` first observes output silence,
   then refreshes liveness and completion state. Silence is not itself completion: a quiet but
   still-running command keeps waiting; a prompt returns `awaiting-input`, and only an actually idle
   shell returns `idle`.

`pairmux doctor` reports the diagnostic tier each shell actually reaches, including `hooks-no-C` —
bash 3.2 emits `A`/`D` but never the `C` (output-start) mark, so completion detection works but
human-interleave correlation is slightly weaker. `hooks-no-C` is not a stored mode: the terminal still
reports `hooks`. With full hooks, completion is correlated to *pairmux's own* command marks, so a human
typing commands in the same pane cannot spoof a completion.

## Bounded and explicit reads

The raw journal is retained in full, but routine observation is deliberately bounded: `peek` reads
the final 64 KiB before applying its line limit, while default `log` reads the final 4 MiB and returns
the last 500 shaped lines. A byte-capped response carries the exact skipped prefix as
`truncated.omitted_bytes`; `omitted_lines` describes line shaping within the bytes actually read.
Its recovery command is executable: `pairmux log <name> --range 1:end`.

Explicit `log --cmd N`, `--grep RE`, and `--range A:B`/`A:end` requests read the complete selected
history. They can produce large replies by design, because the selector is the caller's explicit opt-in.

### awaiting-input detection

Separately, pairmux watches for a command that has gone **quiet with a prompt-shaped last line** — `[y/N]`, `password:`, a pager's `--More--`/`(END)`, "press any key". This refines `running` into the `awaiting-input` status. It is surfaced as a status only; pairmux never auto-answers.

Password/passphrase/passcode prompts are classified as **secrets**. Instead of a "send this answer" hint, the reply says `do NOT guess or type secrets` and points at human handoff (`wait --human --notify`). See [human collaboration](./guides/human-collaboration.md).

## Self-teaching output

Weak models don't reliably remember documentation, so actionable replies embed an ordered `next`
list. Entries may be safety instructions, placeholders, or commands: obey them in order, substitute
real values, then execute the first applicable command. Truncated output ships `get_full`; an awaiting-input reply ships a `send` example (or
the secret-handoff rule); a busy terminal reports who holds the lock. The skill teaches the workflow;
the reply teaches the next step.

## Concurrency and locking

- **Reads** (`peek`, `log`, `ls`, `wait`) are lock-free and record nothing.
- **Command writes** (`run`) take a per-terminal `flock`. When it is contended, pairmux returns an
  `E_BUSY` envelope immediately, naming the holder's pid — it does not queue.
- **Interactive input** (`send`) intentionally bypasses that lock so it can answer a program while
  the corresponding `run` call is blocked. The one-answer rule prevents duplicate input.
- **Creation** uses a separate per-name reservation, so concurrent `new --name X` calls create exactly
  one pane.
- **Humans** type through native tmux and bypass locks. A `note` is journaled; attaching alone is not
  an event, so the human leaves a note when handing control back.

## Statuses and modes

The full status and mode tables live in the [CLI Reference](./cli-reference.md#statuses). In short: a terminal is `idle` / `running` / `awaiting-input` / `dead`, and each terminal runs in `hooks` or `sentinel` completion mode.
