---
title: Multi-agent sharing
description: Lock-free reads, the per-terminal writer lock, and busy envelopes.
---

# Multi-agent sharing

Because journal reads take no lock, **any number of agents can share a terminal as an information
source**. Concurrent `run` calls are serialized by a per-terminal writer lock. `send`, `note`, and a
human typing through tmux deliberately bypass that lock, so they require their own coordination rules.

## Reads are lock-free

`peek`, `log`, `ls`, and `wait` never take the writer lock and never record journal events. Run them
from as many agents as you like against the same terminal.

First, Agent A creates the shared terminal:

```bash
pairmux --json new --name dev
```

Because `wait --pattern` observes only output written after the wait begins, Agent B arms the waiter
before the readiness line can be emitted:

```bash
# Agent B: this call blocks while Agent A continues independently.
pairmux --json wait dev --pattern "compiled successfully" --timeout 30s
```

While B is waiting, Agent A starts a reproducible long-lived demo:

```bash
pairmux --json run dev 'sleep 2; printf "compiled %s\n" successfully; while :; do sleep 60; done' --timeout 500ms
```

The emitted output is `compiled successfully`, but that exact phrase is absent from the echoed
command line, so the waiter cannot report a false readiness match.

Agent C can inspect the same journal without contending with either call:

```bash
pairmux --json log dev --grep "error|warn"
pairmux --json peek dev
```

If the process was already running before an observer arrived, use `log --grep` to search history
before arming a future-only pattern wait. To close the small gap between a history read and a later
wait, start the waiter concurrently before the action that emits the line, or search history again
after a wait timeout.

## `run` takes a per-terminal lock

`run` takes a per-terminal writer `flock`. If another agent concurrently invokes
`pairmux --json run dev "echo second"` while the lock is held, pairmux returns an `E_BUSY` envelope
**immediately** — it does not queue — naming the holder's pid:

```json
{"schema":"pairmux.v1","ok":false,"status":"error","next":["pairmux peek dev"],"error":{"code":"E_BUSY","message":"another writer holds the lock: journal: write.lock held by pid 22018: journal: write lock held by another process","hint":"pairmux peek dev"}}
```

A related `E_BUSY` appears if you try to `run` a second command while a prior one is still running:

```text
error  E_BUSY
a command is still running
next:
  pairmux peek dev
```

Because it returns instead of blocking, the caller should use `pairmux wait NAME` or `peek` to observe
the current work, then retry only after the returned status allows it. Do not sleep-and-guess. Reading
with `peek`/`log`, or using a different terminal, remains available. `ls` shows the lock holder inline
in the `CMD` column:

`send` deliberately does not take the writer lock: its purpose is to answer the pending interactive
program left by `run`. Send each answer once, then `wait` and `peek` rather than repeating input.
`note` also bypasses the writer lock and appends a side-channel event. Neither command authorizes a
second agent to interfere with arbitrary terminal input.

```text
ok
  NAME  STATUS   MODE   CMD
  dev   running  hooks  sleep 8  [lock:22018]  (2026-07-20T09:14:03Z)
```

## A division-of-labor recipe

Create one terminal for each role that executes commands; a read-only observer does not need its own
terminal:

```bash
# Agent A
pairmux --json new --name api

# Agent B
pairmux --json new --name tests
```

Agent C arms readiness observation on `api`, then Agent A starts the service concurrently:

```bash
# Agent C (start this first)
pairmux --json wait api --pattern "service-ready-42" --timeout 30s
```

```bash
# Agent A, while C is blocked
pairmux --json run api 'sleep 2; printf "service-ready-%s\n" "$((40+2))"; while :; do sleep 60; done' --timeout 500ms
```

Agent B can run its independent work without touching `api`:

```bash
pairmux --json run tests 'printf "tests passed\n"'
```

## Rules of thumb

- **Separate command runners into separate terminals.** The writer lock arbitrates `run`, not every
  form of terminal input.
- **Treat `E_BUSY` as routine.** It is a normal envelope, not a crash — read the holder and choose to wait or read-only.
- **Reads avoid writer contention.** Favor `peek`/`log`/`wait` for observation; they take no writer
  lock and append no journal events.
- **Coordinate `send`, `note`, and humans separately.** They bypass the writer lock. A human leaves a
  journaled `note` when handing control back; attach itself is not an event. See
  [human collaboration](./human-collaboration.md).

Clean up the persistent demo terminals when finished; their journals remain available:

```bash
pairmux --json kill api
pairmux --json kill dev
pairmux --json kill tests
```
