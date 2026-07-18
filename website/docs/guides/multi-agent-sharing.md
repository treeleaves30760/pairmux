---
title: Multi-agent sharing
description: Lock-free reads, the per-terminal writer lock, and busy envelopes.
---

# Multi-agent sharing

Because the journal is a plain file and reads take no lock, **any number of agents can share a terminal as an information source**, while writes are serialized by a per-terminal lock. This makes patterns like "one agent runs the dev server, others watch its log" work with no coordination.

## Reads are lock-free

`peek`, `log`, `ls`, and `wait` never take a lock and never record anything. Run them from as many agents as you like, concurrently, against the same terminal:

```bash
# Agent A owns the dev server:
pairmux new --name dev
pairmux run dev "npm run dev" --timeout 3s

# Agents B and C watch it, independently, with zero contention:
pairmux wait dev --pattern "compiled successfully"
pairmux log dev --grep "error|warn"
pairmux peek dev
```

## Writes take a per-terminal lock

`run` and `send` take a per-terminal writer `flock`. Only one writer acts at a time. If a second writer arrives while the terminal is held, pairmux returns an `E_BUSY` envelope **immediately** — it does not queue — naming the holder's pid:

```json
{"schema":"pairmux.v1","ok":false,"status":"error","next":["pairmux peek build"],"error":{"code":"E_BUSY","message":"another writer holds the lock: journal: write.lock held by pid 22018: journal: write lock held by another process","hint":"pairmux peek build"}}
```

A related `E_BUSY` appears if you send a command while a prior one on that terminal is still running:

```text
error  E_BUSY
a command is still running
next:
  pairmux peek build
```

Because it returns instead of blocking, **the caller decides**: wait a moment and retry, fall back to read-only (`peek`/`log`), or use a different terminal. `ls` shows the current holder inline:

```text
   NAME   STATUS   MODE   LOCK   AGE  CMD
   build  running  hooks  22018  3s   sleep 8
```

## A division-of-labor recipe

```bash
# One terminal per role, so no two agents ever contend for a writer lock:
pairmux new --name api      # agent A: runs the server
pairmux new --name tests    # agent B: runs the test suite
pairmux new --name logs     # agent C: tails and greps, read-only

pairmux run api "go run ./cmd/server" --timeout 3s
pairmux wait api --pattern "listening"     # B and C wait for readiness, lock-free
pairmux run tests "go test ./..."
```

## Rules of thumb

- **Separate writers into separate terminals.** Contention only happens when two agents write to the *same* terminal.
- **Treat `E_BUSY` as routine.** It is a normal envelope, not a crash — read the holder and choose to wait or read-only.
- **Reads never disturb anyone.** Favor `peek`/`log`/`wait` for observation; they are free and invisible to the writer.
- **Humans bypass the lock** (they type through native tmux), but their `attach`/`note` events are journaled so writers stay informed. See [human collaboration](./human-collaboration.md).
