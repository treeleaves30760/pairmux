---
title: Interactive programs
description: REPLs, pagers, confirmation prompts, and the never-guess-secrets rule.
---

# Interactive programs

Some programs stop and wait for input: a `[y/N]` confirmation, a Python REPL, a pager, or a
password prompt. pairmux recognizes common prompt shapes after output has been quiet for 800ms and
reports how the driving agent should proceed. It never answers a prompt automatically.

## awaiting-input

Create a shell terminal first, then run a harmless confirmation command. When the last non-blank line
matches a recognized prompt and stays quiet, the status refines from `running` to `awaiting-input`:

```bash
pairmux --json new --name confirm
pairmux --json run confirm 'printf "Continue? [y/N] "; read answer; printf "answer=%s\n" "$answer"'
```

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"confirm","mode":"hooks","output":"Continue? [y/N] ","next":["pairmux send confirm --text <answer> --enter"]}
```

The JSON examples show a hooks-mode shell. A terminal may instead report `mode: sentinel`; the
prompt and status handling described here is the same.

Answer once with `send`. It writes input but does not wait for the program to process it, so follow it
with `wait` before reading the result:

```bash
pairmux --json send confirm --text y --enter
```

```json
{"schema":"pairmux.v1","ok":true,"status":"sent","terminal":"confirm","mode":"hooks","next":["a command is running; sent input goes to it","pairmux peek confirm"]}
```

```bash
pairmux --json wait confirm --idle 800
pairmux --json peek confirm
```

`wait --idle` can return `idle`, `awaiting-input`, `dead`, or `timeout`; inspect `status` instead of
assuming the answer completed the command.

pairmux recognizes `[y/N]`, `(yes/no)`, `password:`-style prompts, pagers (`--More--`, `(END)`, a bare `:`), and "press any key". It **never auto-answers** — it only reports the state.

## The never-guess-secrets rule

When a recognized prompt is for a **password, passphrase, or passcode**, pairmux classifies it as a
secret. Its `next` field deliberately omits a `send` command and points at human handoff:

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"login","mode":"hooks","output":"\nPassword: ","next":["do NOT guess or type secrets","pairmux wait login --human --notify   # hand off to the human"]}
```

:::danger Never type or guess secrets
This is an agent rule, not an input firewall: the CLI cannot prevent someone from manually invoking
`send`. An agent must not invent a secret, recover one from output, put one in a CLI argument or note,
or send it into the pane. Hand off with `pairmux wait <name> --human --notify`. The full flow is in
[human collaboration](./human-collaboration.md).
:::

pairmux captures pane output, not a separate secret store. Password programs normally disable
terminal echo; if a program echoes typed input, that output can enter the journal. The human should
verify that the prompt hides input before entering a real credential.

## Sending input: text vs keys

`send` applies its parts in order — text, then keys, then a trailing Enter:

- `--text S` sends literal text through tmux's `send-keys -l`; pairmux performs no further shell or
  key-name interpretation. The shell that launches pairmux still expands its own arguments, so use
  shell quoting when needed: `--text '$HOME'` sends the five literal characters `$HOME`.
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

A pager (`git log`, `less`) shows as `awaiting-input` with a `--More--`/`(END)`/`:` last line. Start a
terminal, run the pager, and get out by sending `q` once:

```bash
pairmux --json new --name review
pairmux --json run review "LESS= less /etc/hosts"
pairmux --json send review --text q
pairmux --json wait review --idle 800
```

To avoid pagers entirely, disable them at the source (`git --no-pager log`, `PAGER=cat`) when you create or run in the terminal.

## Python REPL round-trip

```bash
pairmux --json new --name repl --cmd "python3" # reports running; the REPL is live
pairmux --json send repl --text "2 + 2" --enter
pairmux --json wait repl --idle 800              # returns at the next quiet >>> prompt
pairmux --json peek repl                         # now the journal contains "4"
pairmux --json send repl --text "exit()" --enter
pairmux --json wait repl --timeout 10s           # observe the program exit
```

Known REPL, TUI, and persistent-server entrypoints should use `new --cmd`, then
`send` → `wait` → `peek`; an immediate `peek` can race the program's response. A `--cmd` terminal
stays `running` while its pane lives, but `wait --idle` can still return `awaiting-input` for a
recognized quiet prompt. The final wait returns `status: dead` if the pane exits while it is waiting;
if Python exited before the wait began, it returns `E_DEAD` instead. In either case, `log` and `peek`
can still read the retained journal. If the REPL needs shell setup first, use a shell terminal and
`run`.
