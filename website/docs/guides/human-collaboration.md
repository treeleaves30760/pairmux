---
title: Human collaboration
description: attach, watch, and note — how a human steps into an agent's terminal and hands back.
---

# Human collaboration

pairmux terminals are real tmux panes, so a human can watch them, take one over, do something the agent can't or shouldn't, and hand back — all in the same live session. The agent finds out what the human did through **notes**.

## The three human commands

- **`pairmux watch`** — a live dashboard of every terminal: status, lock holder, current command, last activity. Awaiting-input rows are flagged `!!`, dead rows `xx`. `Ctrl-C` quits.
- **`pairmux attach [name]`** — hand yourself a live tmux client, focused on `name`. You are now typing into the same pane the agent uses. (Detach with tmux's `Ctrl-b d`.)
- **`pairmux note <name> <text>`** — leave a message the agent sees on its next `run`/`peek`/`wait`, and which resolves `wait --human`.

```bash
pairmux watch
```

```text
pairmux watch — 15:15:04 — socket pairmux  (Ctrl-C to quit)

   NAME       STATUS          MODE   LOCK   AGE  CMD
   build      running         hooks  22018  3s   sleep 8
!! dbmigrate  awaiting-input  hooks  -      6s   printf 'Password: '; read -s p; echo
   dev        idle            hooks  -      1s   -
```

## The handoff story

This is the canonical pairing loop: an agent hits a password it must not guess, summons a human, the human answers in the pane, and the agent resumes — knowing the human helped.

**1. The agent hits a secret prompt.** Running a migration, the terminal goes to `awaiting-input` with a secret-class prompt, so the reply forbids guessing and points at handoff:

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"dbmigrate","mode":"hooks","output":"\nPassword: ","next":["do NOT guess or type secrets","pairmux wait dbmigrate --human --notify   # hand off to the human"]}
```

**2. The agent hands off and blocks**, firing a desktop notification:

```bash
pairmux wait dbmigrate --human --notify
```

`wait --human` blocks until a human leaves a note; `--notify` pops a desktop notification (`osascript` on macOS, `notify-send` on Linux — best-effort). The agent's tool call is now parked, waiting.

**3. The human takes over the same pane, types the secret, and leaves a note:**

```bash
pairmux attach dbmigrate            # you land in the live pane; type the password
pairmux note dbmigrate "entered the db password"
```

**4. The agent's `wait` unblocks the instant the note lands:**

```json
{"schema":"pairmux.v1","ok":true,"status":"human-done","terminal":"dbmigrate","mode":"hooks","output":"entered the db password","next":["pairmux peek dbmigrate"]}
```

**5. The agent resumes** with `pairmux peek dbmigrate` and continues. The password was never seen by, echoed to, or logged for the agent.

## Notes flow both ways

A note is a general side channel, not just for handoffs. A human (or another agent) can leave context at any time:

```bash
pairmux note build "use the staging token, not prod"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"noted","terminal":"build","next":["the agent sees this note on its next run/peek/wait of build"]}
```

The agent sees it on its next command, in the `notes` field:

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"resumed","notes":["use the staging token, not prod"]}
```

`ls` shows a `[notes:N]` badge for terminals with unseen notes, so a human can see what's waiting to be picked up.

:::note
`wait --human` also returns immediately if a note is *already* waiting and unseen — the natural "human notes, then the agent waits" ordering never drops a message.
:::

## When the agent should wait for the human

While a human is typing in a pane, the agent should not fight them for it. The discipline: if a human
has attached, `wait` (don't `run`) until they leave a note. The note is recorded and surfaces in the
next reply; attaching alone is deliberately just a live tmux operation and creates no journal event.
