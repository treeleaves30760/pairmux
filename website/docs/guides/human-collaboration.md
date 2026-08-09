---
title: Human collaboration
description: attach, watch, and note — how a human steps into an agent's terminal and hands back.
---

# Human collaboration

pairmux terminals are real tmux panes, so a human can watch them, take one over, perform an action
the agent cannot or should not perform, and hand back in the same live session. A **note**, rather
than attach/detach itself, is the explicit hand-back signal.

## The three human commands

- **`pairmux watch`** — a live dashboard of every terminal: status, lock holder, current command, last activity. Awaiting-input rows are flagged `!!`, dead rows `xx`. `Ctrl-C` quits.
- **`pairmux attach [name]`** — from an interactive terminal outside tmux, open a tmux client focused
  on `name`. Detach with tmux's `Ctrl-b d`. The command refuses non-TTY output and nested tmux. If
  already inside tmux, detach to the outer shell or use another terminal before running `attach`;
  `switch-client` cannot cross to pairmux's separate named socket.
- **`pairmux note <name> <text>`** — append a non-secret message. `run` and `peek` expose unseen notes
  in `notes`; `wait --human` returns one as `status: human-done` with the note text in `output`.
- **Subscribe** — `wait --done` blocks until the terminal's command finishes and reports its
  `exit_code`, whoever started it. Any number of agents can hold one at once.

```bash
pairmux watch
```

```text
pairmux watch — 15:15:04 — socket pairmux  (Ctrl-C to quit)

   NAME       STATUS          MODE   LOCK   AGE  CMD
   build      running         hooks  22018  3s   sleep 8
!! review      awaiting-input  hooks  -      6s   printf 'Human approval [y/N] '; read answer
   dev        idle            hooks  -      1s   -
```

## The handoff story

This reproducible loop uses a harmless approval prompt. The agent does not answer on the user's
behalf: it summons a human, the human answers in the pane, then leaves a note so the agent knows
control has returned.

**1. The agent creates a terminal and reaches a prompt that requires human authority:**

```bash
pairmux --json new --name review
pairmux --json run review 'printf "Human approval required [y/N] "; read answer; printf "answer=%s\n" "$answer"'
```

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"review","mode":"hooks","output":"Human approval required [y/N] ","next":["pairmux send review --text <answer> --enter"]}
```

The example shows `mode: hooks`; another supported shell may report `mode: sentinel` while keeping
the same handoff states.

The CLI describes how input could be sent, but the agent's authorization policy requires a human to
make this decision.

**2. The agent hands off and blocks**, firing a desktop notification:

```bash
pairmux --json wait review --human --notify --timeout 5m
```

`--notify` uses `osascript` on macOS or `notify-send` on Linux and is best-effort. `wait --human`
returns on a note, on the handoff resolving without one (below), on pane death, or on the deadline;
its default timeout is 300 seconds. A `status: timeout` means the human has not come yet: the `next`
repeats that exact wait — conditions preserved, deadline doubled, never under 300 seconds — so
follow it and keep waiting instead of sleeping or acting alone.

**3. The human takes over, answers, detaches, and leaves a non-secret note:**

```bash
pairmux attach review # type y in the pane, then detach with Ctrl-b d
pairmux --json note review "approved; entered y and detached"
```

**4. The agent's `wait` returns on the next polling pass after the note lands:**

```json
{"schema":"pairmux.v1","ok":true,"status":"human-done","terminal":"review","mode":"hooks","output":"approved; entered y and detached","next":["pairmux peek review"]}
```

**5. The agent resumes** by following `next`:

```bash
pairmux --json peek review
```

## Secret prompts

For recognized secret prompts — password/passphrase/passcode, PIN, one-time and verification
codes, API keys, tokens, and localized sudo password prompts — pairmux removes `send` from the
suggested next steps and recommends handoff (recognition is best-effort; a known credential prompt
sitting quiet at `running` deserves the same handoff even without the classification):

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"login","mode":"hooks","output":"Password: ","next":["do NOT guess or type secrets","pairmux wait login --human --notify   # hand off to the human"]}
```

This is guidance, not a credential vault or input firewall. The agent must not put a secret in
`send`, `note`, command-line arguments, or journal searches. pairmux captures pane output; password
programs normally suppress terminal echo, but a program that echoes typed input can put it in the
journal. Before entering a real secret, the human must confirm that the prompt hides input.

## Notes flow both ways

A note is a general side channel, not just for handoffs. A human (or another agent) can leave context at any time:

```bash
pairmux --json new --name build
pairmux --json note build "reviewed the failure; retry the local fixture"
pairmux --json peek build
```

The `peek` response includes the note in `notes`; these are the relevant fields (the full response
also carries the usual `pairmux.v1` fields and may include shell output):

```json
{"status":"idle","notes":["reviewed the failure; retry the local fixture"]}
```

`ls` shows a `[notes:N]` badge for terminals with unseen notes, so a human can see what's waiting to be picked up.

:::note
### When the human leaves no note

A human who answers a prompt and walks away records nothing at all — attaching is a live tmux
operation, and typing into the pane is not an event. So `wait --human` also ends the moment the
terminal is visibly moving again: `status: running`, whose `next` offers `wait --done` for following
the command the rest of the way. The distinction is deliberate — a password answered at second two
can be followed by a five-minute migration, and the agent is released at second two, not at minute
five. When the command instead finishes without printing anything after the answer, or had already
finished, the reply is `status: done` with its `exit_code`. A wrong password that re-prompts keeps
the wait blocked — the terminal is still awaiting input.

Waiting late is fine too: hand off, go do other work, and a completion that already landed returns
from the next `wait --human` immediately. Like a note, it is not consumed by reading — until the
agent's next `run` settles the command, another `wait --human` can return it again.

Neither of those outcomes carries `output`. The span they would quote is the span the human typed
into, and a handoff exists precisely so the agent never sees it; the agent gets the fact and the exit
code, and `peek`s if it needs more. A `note` is still the only way to tell the agent *what* you did.

`wait --human` also returns immediately if a note is already waiting and unseen. `peek` and `wait`
are read-only, so neither consumes it: another `wait --human` can return the same note again. A later
completed `run` records `cmd_end`; notes older than that event are then considered seen.
:::

Ordinary `wait --idle` and `wait --pattern` do not copy notes into `notes`. Use `peek`/`run` to read
that field, or use `wait --human` when the note itself is the condition; the latter returns the note
in `output`, not `notes`.

## When the agent should wait for the human

While a human is typing in a pane, the agent should not fight them for it. Once the agent initiates a
handoff, it should `wait --human` rather than `run` or repeatedly `send`. Attaching alone is a live
tmux operation and creates no event, so the note — not detach — is the machine-visible hand-back
signal.
