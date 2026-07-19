---
title: Interactive programs
description: REPLs, pagers, confirmation prompts, and the never-guess-secrets rule.
---

# Interactive programs

Some programs stop and wait for input: a `[y/N]` confirmation, a Python REPL, a pager, a password prompt. pairmux detects when a terminal is waiting and tells you how to respond — with one hard rule for secrets.

## awaiting-input

When a command goes quiet and its last screen line looks like a prompt, the status refines from `running` to `awaiting-input`, and `next` shows how to answer:

```bash
pairmux run deploy "terraform apply"
```

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"deploy","mode":"hooks","output":"\nDo you want to continue? [Y/n] ","next":["pairmux send deploy --text <answer> --enter"]}
```

Answer with `send`:

```bash
pairmux send deploy --text yes --enter
```

```json
{"schema":"pairmux.v1","ok":true,"status":"sent","terminal":"deploy","mode":"hooks","next":["pairmux peek deploy"]}
```

pairmux recognizes `[y/N]`, `(yes/no)`, `password:`-style prompts, pagers (`--More--`, `(END)`, a bare `:`), and "press any key". It **never auto-answers** — it only reports the state.

## The never-guess-secrets rule

When the prompt is for a **password, passphrase, or passcode**, pairmux classifies it as a secret and refuses to offer an answer. Instead it points at a human handoff:

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"dbmigrate","mode":"hooks","output":"\nPassword: ","next":["do NOT guess or type secrets","pairmux wait dbmigrate --human --notify   # hand off to the human"]}
```

:::danger Never type or guess secrets
An agent must not invent a password or pull one from output. Hand off to a human with `pairmux wait <name> --human --notify`. The full flow is in [human collaboration](./human-collaboration.md).
:::

## Sending input: text vs keys

`send` applies its parts in order — text, then keys, then a trailing Enter:

- `--text S` sends **literal** text (via `send-keys -l`), so `$HOME`, `;`, and quotes are not interpreted.
- `--key K` sends a named key. Repeatable. Valid keys: `Enter Escape Tab Space Up Down Left Right Home End PPage NPage BSpace DC`, `F1`–`F12`, `C-a`..`C-z`, `M-a`..`M-z`.
- `--enter` appends a final Enter.

```bash
pairmux send repl --text "print('hi')" --enter    # type a line and run it
pairmux send app  --key C-c                        # interrupt (Ctrl-C)
pairmux send menu --key Down --key Down --key Enter # navigate a TUI
```

:::note
Passing a plain character as a key is a common mistake. `--key q` is rejected — use `--text q` to type the letter, or a named key like `--key Enter`. The error hint says exactly this.
:::

## Working with a pager

A pager (`git log`, `less`) shows as `awaiting-input` with a `--More--`/`(END)`/`:` last line. Get out of it by sending `q`:

```bash
pairmux send review --text q     # quit the pager
```

To avoid pagers entirely, disable them at the source (`git --no-pager log`, `PAGER=cat`) when you create or run in the terminal.

## Python REPL round-trip

```bash
pairmux new --name repl --cmd "python3" # reports running; the REPL is live
pairmux send repl --text "2 + 2" --enter
pairmux peek repl                    # see "4"
pairmux send repl --text "exit()" --enter
```

Known REPL, TUI, and persistent-server entrypoints should use `new --cmd`, then `send` + `peek`. If
the REPL needs shell setup first, use a shell terminal and `run`; a recognized quiet prompt such as
Python's `>>>` returns `awaiting-input` so you can continue with `send`.
