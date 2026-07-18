# Troubleshooting

Recovery recipes for the states you'll actually hit. Every error envelope carries a `hint`/`next` that
usually tells you the fix — read it first.

## E_BUSY — the terminal is taken

Two causes, same code:

**A command is still running** — you tried a second `run` (or `run` while a prior command is going):
```json
{"schema":"pairmux.v1","ok":false,"status":"error","next":["pairmux peek build"],"error":{"code":"E_BUSY","message":"a command is still running","hint":"pairmux peek build"}}
```
Fix: `peek` to see what's running, then `wait` for it to finish — or open a **separate** terminal with
`pairmux new` for parallel work. One command runs per terminal at a time.

**Another writer holds the lock** — a different agent/process is writing this terminal:
```json
{"schema":"pairmux.v1","ok":false,"status":"error","error":{"code":"E_BUSY","message":"another writer holds the lock: … held by pid 22018 …","hint":"pairmux peek build"}}
```
`E_BUSY` returns **immediately** — pairmux never queues. You decide: wait a moment and retry, fall
back to read-only (`peek`/`log`, which never take the lock), or use a different terminal. `ls` shows
the holder pid inline.

## A hung / stuck command

If a command hangs (infinite loop, waiting on something that won't come), interrupt it with Ctrl-C,
then reuse the terminal. Confirm the terminal has actually gone `idle` before you run the next
command — the interrupt is asynchronous:
```bash
pairmux run stuck "echo start; sleep 30" --timeout 2s   # -> status: running
pairmux send stuck --key C-c                             # interrupt (returns immediately)
pairmux wait stuck --idle 800                            # wait for it to settle -> status: idle
pairmux run stuck "echo recovered"                       # -> status: done
```
If you skip the `wait` and run too soon, you'll get `E_BUSY` ("a command is still running") because the
interrupt hasn't landed yet — just `pairmux wait stuck --idle 500` and retry the `run`. If Ctrl-C
doesn't free it at all, escalate: `--key C-\\` (SIGQUIT) or, last resort, `pairmux kill stuck` and
`pairmux new` a fresh one (the journal is retained either way).

## E_DEAD — the pane is gone

The terminal's tmux pane died (someone closed it, the shell exited, tmux was killed):
```json
{"schema":"pairmux.v1","ok":false,"status":"error","error":{"code":"E_DEAD","message":"terminal \"build\" is dead","hint":"pairmux new --name build"}}
```
The journal is still on disk — `pairmux log build` still works for post-mortem. To keep going, `new` a
fresh terminal (reuse the name after `kill`, or pick another).

## E_NO_TERMINAL — wrong name

```json
{"schema":"pairmux.v1","ok":false,"status":"error","next":["pairmux ls"],"error":{"code":"E_NO_TERMINAL","message":"no terminal \"biuld\"; existing: build, deploy","hint":"pairmux ls"}}
```
The message lists the names that **do** exist — you probably have a typo. `pairmux ls` to confirm.

## `wait --pattern` returned `timeout` but the text is right there

`--pattern` matches output produced **after the wait starts** — it does not scan what was already
printed. A common trap: a dev server prints `Serving HTTP …` during the initial `run`, then you
`wait --pattern "Serving HTTP"` — which times out, because that line is already in the past.

```json
{"schema":"pairmux.v1","ok":true,"status":"timeout","terminal":"api","mode":"hooks","next":["pairmux peek api","pairmux wait api --timeout 16s"]}
```

Fixes:
- If the readiness line **may already be present**, read it instead of waiting: check the `run`
  output, or `pairmux peek api` / `pairmux log api --grep "Serving HTTP"`.
- Use `--pattern` only for a line you expect to appear in the **future** (a slow boot, a later log
  event). Start the `wait` before the event, then trigger it.

## `wait --idle` returned too early

`--idle MS` resolves on *any* silence of `MS` ms — including a command that is merely paused (a
`sleep`, waiting on I/O, blocked on a prompt). It is a backstop for "the program is still running but
I want to look now", not a reliable "the command finished" signal.

For "did it finish?", rely on `run` itself (hooks give a precise `done` + `exit_code`), or `wait
--pattern` on a known completion line. If `--idle` returns and `peek` still shows `running`, the
command isn't done — keep waiting or read on.

## Output was truncated

Never re-run to see more — the journal already has everything. Follow the `truncated.get_full`
pointer, or query the journal directly:
```bash
pairmux log build --cmd 3          # the whole command's output
pairmux log build --grep "error|FAIL"   # just matching lines, with line numbers
pairmux log build --range 400:460  # a specific line range
```
`--grep` is capped at 200 matches; narrow the regex or use `--range` for very dense logs.

## Sentinel mode / `--cmd` terminals

A terminal created with `--cmd`, or one whose shell couldn't take OSC 133 hooks, reports
`mode:"sentinel"`. Completion detection still works (via an injected marker), but such terminals are
**programs you drive with `send`/`peek`, not `run`**. `run` targets an interactive shell; for a
`--cmd` program, interact with `send` and observe with `peek`/`wait`/`log`.

## Environment check

If terminals behave oddly (no completion detected, notifications missing), run the doctor:
```bash
pairmux doctor
```
It reports tmux version (needs **≥ 3.2**), state-dir writability, the per-shell completion tier
(`hooks` / `hooks-no-C` / `sentinel`), and whether a desktop notifier is available. `hooks-no-C` means
completion works but human-interleave correlation is slightly weaker (e.g. bash 3.2). pairmux is
macOS/Linux only; on Windows use WSL.
