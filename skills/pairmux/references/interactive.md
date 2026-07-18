# Interactive programs playbook

Some programs stop and wait for input: a `[y/N]` confirmation, a REPL, a pager, a password prompt.
pairmux detects when a terminal is waiting and refines its status from `running` to `awaiting-input`,
with one hard rule for secrets.

## awaiting-input

When a command goes quiet and its last screen line looks like a prompt, `status` becomes
`awaiting-input` and `next` shows how to answer:

```bash
pairmux run deploy "terraform apply"
```
```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"deploy","mode":"hooks","output":"\nDo you want to continue? [Y/n] ","next":["pairmux send deploy --text <answer> --enter"]}
```
```bash
pairmux send deploy --text yes --enter
```

pairmux recognizes `[y/N]`, `(yes/no)`, `password:`-style prompts, pagers (`--More--`, `(END)`, a
bare `:`), and "press any key". It **never auto-answers** — it only reports the state, and you decide.

## The never-guess-secrets rule

When the prompt is for a **password, passphrase, or passcode**, pairmux classifies it as a secret and
refuses to offer an answer. It points at a human handoff instead:

```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"dbmigrate","mode":"hooks","output":"\nPassword: ","next":["do NOT guess or type secrets","pairmux wait dbmigrate --human --notify   # hand off to the human"]}
```

**Do not invent a password, and do not pull one out of earlier output or the environment.** Hand off:

```bash
pairmux wait dbmigrate --human --notify
```

This blocks your tool call and pings the human's desktop. The full loop is in
[collaboration.md](collaboration.md). The password is typed by the human, straight into the pane —
you never see, echo, or log it.

## send: text vs keys

`send` applies its parts in order — text, then keys, then a trailing Enter:

- `--text S` sends **literal** text (via `send-keys -l`), so `$HOME`, `;`, and quotes are not
  interpreted.
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

After a `send`, the terminal is running again; the answer went to the program. Do **not** re-send the
same answer or spam Enter — `peek` to see the result:

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

A REPL never "finishes", so `run` will report `running`/`awaiting-input`, not `done`. Drive it with
`send` + `peek`:

```bash
pairmux new --name repl
pairmux run repl "python3"            # returns running; the REPL is now live
pairmux send repl --text "2 + 2" --enter
pairmux peek repl                      # see "4"
pairmux send repl --text "exit()" --enter
```

Tip: for a program you know is a REPL up front, create the terminal with `--cmd` so pairmux starts in
`sentinel` mode and you drive it purely with `send`/`peek`:

```bash
pairmux new --name repl --cmd "python3"
pairmux send repl --text "print(6*7)" --enter
pairmux peek repl
```

## Full-screen TUIs

For a full-screen app (vim, htop, a menu), the journal tail is noisy — use the screen render instead:

```bash
pairmux peek tui --screen     # a capture-pane snapshot of the current viewport
pairmux send tui --key Down --key Enter
pairmux send tui --key Escape
```
