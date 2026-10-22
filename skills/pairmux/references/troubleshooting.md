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
`E_BUSY` returns **immediately** — pairmux never queues. Inspect `ls`/`peek`/`log`, then use an
appropriate blocking wait (`--done --idle 800` for the current shell command) or another terminal.
Do not build a sleep/retry polling loop. `ls` shows the holder pid inline.

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
If you skip the `wait` and run too soon, you'll get `E_BUSY` because the interrupt has not landed.
Wait again and inspect the returned state: `awaiting-input` is not `idle`, and timeout is not recovery.
Ctrl-C is asynchronous and may be handled/ignored by the program. If it cannot be interrupted, use
only an authorized program-specific exit or escalate to the human; kill/recreate is a last resort
when a fresh terminal is explicitly acceptable. Do not offer unsupported control keys: `--key`
accepts `C-a`..`C-z`, not arbitrary signal shortcuts.

For a `new --cmd` program terminal, Ctrl-C does not turn it into a shell. Continue with `send`/`peek`
and an idle/event wait, or observe `dead` if the program exits; `run` remains invalid.

## E_DEAD — the pane is gone

The terminal's pane died (someone closed it, the shell exited, the tmux or built-in server was
killed):
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

## Startup is silent, unknown, or returned `running` / `timeout`

Submit the command first, then estimate time to the first meaningful signal, not total runtime.
Generally make the initial observation bounded to 30s, shorter for fast tasks; `run --timeout 30s`
can serve as that observation. Inspect returned output/status, or a safe `peek` if needed. If `run`
already returned enough evidence of completion or healthy startup, do not add a redundant wait.

Before unrelated work or a long wait, verify expected progress/readiness and look for early errors or
prompts. Unknown/silent is not healthy, and `running` only means the command remains active. A timeout
is not failure either: investigate the missing signal, rather than treating silence as success or
blindly following a longer `next` wait. For a shell needing more observation, use a bounded
`wait --done --idle 800 --timeout 30s` (shorter when appropriate). Programs use bounded `--idle`,
future `--pattern`, or `--note` waits, never `--done`; read readiness already printed instead.

Prefer event-aware bounded `run`/`wait`. A brief sleep is only a fallback when no suitable blocking
observation is available, followed by a safe check, not repeated blind polling. Long subscriptions
such as 600s fit verified healthy work or intentional passive listening, not an unchecked startup.
**Credential handoffs are excluded from the 30s startup rule**: keep `--human --notify` at ≥300s,
repeat after interruption/timeout, and never read secret spans to check startup.

## `wait --pattern` returned `timeout` but the text is right there

`--pattern` matches output produced **after the wait starts** — it does not scan what was already
printed. A common trap: a dev server prints `Serving HTTP …` during the initial `run`, then you
`wait --pattern "Serving HTTP"` — which times out, because that line is already in the past.

```json
{"schema":"pairmux.v1","ok":true,"status":"timeout","terminal":"api","mode":"hooks","next":["pairmux peek api","pairmux wait api --pattern \"Serving HTTP\" --timeout 16s"]}
```

Fixes:
- If the readiness line **may already be present**, read it instead of waiting: check the `run`
  output, or `pairmux peek api` / `pairmux log api --grep "Serving HTTP"`. `new --cmd` returns a
  status, not startup output; read `peek`/`log` after creating the program.
- Use `--pattern` only for a line you expect to appear in the **future** (a slow boot, a later log
  event). Start the `wait` before the event, then trigger it.

## A quiet command did not satisfy `wait --idle`

This is intentional. `--idle MS` first observes `MS` ms of output silence, then refreshes terminal
liveness and status. A command that is sleeping, blocked on I/O, or otherwise still running remains
`running`; pairmux keeps waiting until the shell is actually idle or the overall timeout expires.
A prompt returns `awaiting-input`, and a vanished pane returns `dead`.

For finite **shell** commands, use `run`, then `wait --done --idle 800` if needed: exact completion
races verified idle/prompt detection, without sleeping. For long-lived programs, read already-printed
readiness or use `wait --pattern` for a future event. Silence in a program terminal does not invent
shell completion; raw-mode inferred `awaiting-input` is not proof a turn finished or input is safe.

This idle/prompt behavior is armed only when idle is the default condition or `--idle` is explicit.
`wait --pattern ...` and `wait --done` keep blocking at a prompt until their requested condition,
pane death, or timeout. Add `--idle MS` when either condition should race idle/prompt detection.

`wait --human` is the exception: a prompt never resolves it, with or without `--idle`. Handing off
*because* of a prompt and then being handed that prompt straight back is a spin, so `--human` waits
for the prompt to be **answered** — a human `note`, the terminal visibly moving again (`running`),
or the command completing (`done`). If it times out, the human has not come yet: the `next` repeats
the same wait with a longer deadline, and following it is the correct move. If the tool client
interrupts, reissue that handoff immediately; keep the 300s default or an explicit duration ≥300s.
A resumed program uses `--idle 800`, never `--done`. A `human-done` note alone is not completion;
check state with `ls` before another wait, because pure `--done` at idle subscribes to a future
command. Do not automatically `peek` a sensitive span: handoff suppression is not redaction or
journal secrecy. Never put credentials in notes. See [collaboration.md](collaboration.md).

## A note from inside a pane says E_NO_TERMINAL or wakes the wrong waiter

`PAIRMUX_SOCKET` inside the pane is its child endpoint, not its owner. Have the creator supply the
owning endpoint and terminal name explicitly: a worker owned by `team` calls
`pairmux --socket team note worker "turn complete"`; its coordinator waits with
`pairmux --socket team wait worker --note`. Do not parse a parent socket from the child name or
invent an environment variable. See [the runnable recipe](collaboration.md#notes-from-inside-a-pane-owning-socket-recipe).

## E_TMUX — the terminal backend failed

`E_TMUX` (historical name) means the backend failed: tmux, or pairmux's built-in server. Run
`pairmux doctor`; its first line says which backend is in use, why, and the socket path or tmux
version.

**tmux backend.** If tmux reports `Permission denied`, `No such file or directory`, or
`File name too long` under `/tmp/tmux-*`, point its socket root at a short writable directory and
retry:

```bash
export TMUX_TMPDIR="$TMPDIR"
pairmux doctor
```

pairmux honors `TMUX_TMPDIR`. This also avoids macOS sandbox restrictions and the Unix socket path
length limit.

**Built-in backend.** Its socket, lock, and log live in a runtime directory: on Unix
`$PAIRMUX_TMPDIR`, else `$TMUX_TMPDIR`, else `/tmp`, then `pairmux-<uid>/` (must be yours and mode
0700); on Windows `%PAIRMUX_TMPDIR%`, else `%LOCALAPPDATA%\pairmux`, then `run\`. A "runtime
directory" / "too deep" / "unsafe runtime directory" / permission error means: set `PAIRMUX_TMPDIR`
to a short writable directory you own and retry (socket paths are limited to about 100 bytes; pairmux
refuses a directory another user owns or can reach). If the server "did not start",
read `<socket>.log` in that directory (the error names it) and run `pairmux doctor`.

Keep the same `PAIRMUX_BACKEND`, `PAIRMUX_TMPDIR`/`TMUX_TMPDIR`, `PAIRMUX_SOCKET`, and
`PAIRMUX_STATE_DIR` in every process that needs to share a terminal.

## A terminal I created is missing from `ls`

The two backends keep separate terminals and state, and `ls` lists only the selected backend's
terminals (live or dead). Automatic selection prefers a built-in server already running for the
socket (unless a tmux server runs there too), then tmux 3.2+ if it is on PATH, so a different
environment (another `PAIRMUX_BACKEND`, or `PATH` without tmux) can select the other backend. Check `pairmux doctor`'s `backend` line, and use
the same `PAIRMUX_BACKEND` (`tmux` or `builtin`) as the process that created the terminal. An invalid
`PAIRMUX_BACKEND` is `E_BAD_ARGS`; `PAIRMUX_BACKEND=tmux` on Windows is an error.

## Output was truncated

Never re-run to see more — the journal already has everything. Follow the `truncated.get_full`
pointer, or query the journal directly:
```bash
pairmux log build --cmd 3          # the whole command's output
pairmux log build --grep "error|FAIL"   # just matching lines, with line numbers
pairmux log build --range 400:460  # a specific line range
pairmux log build --range 1:end    # every shaped line (can be large)
```
`peek` and default `log` intentionally read bounded journal tails and report both line shaping and any
skipped raw-byte prefix. Explicit `--cmd`, `--grep`, and `--range` selectors read the complete selected
history and can return large results, so prefer a narrow regex or bounded range for dense logs.

## Journal grew huge / state dir eating disk

Journals are retained on `kill` so post-mortem `log` keeps working; the flip side is that a chatty
dev server or `tail -f` grows its `raw.log` indefinitely, and dead terminals keep their history
until you reclaim it. When a reply warns `journal is large (…MB)`:

```bash
pairmux kill chatty && pairmux new --name chatty   # rotate: old journal becomes chatty.prev
pairmux prune chatty                                # reclaim the archived journal's disk
pairmux prune --older-than 7d --dry-run             # sweep preview: all dead terminals + archives
pairmux prune                                       # actually reclaim
```

Pruned history is unrecoverable — `log --cmd N` for anything you still need first. `pairmux doctor`
shows total retained bytes and the largest terminals.

## Sentinel mode / `--cmd` terminals

A terminal created with `--cmd` reports `mode:"sentinel"` but is a **program**, not a shell:
`run` and `wait --done` return `E_BAD_ARGS`. Use `send`/`peek`, `--idle`, future `--pattern`, or explicit
owning-socket `--note`. Its exit is `dead`, not `done`.

An unknown interactive **shell**, or a shell whose OSC 133 probe failed, can also report `sentinel`.
There, injected markers still support `run`/`--done`: POSIX-like shells carry `$?`, Fish uses `$status`,
and `cmd.exe` uses `ERRORLEVEL`. `mode` alone does not identify the terminal kind.

## Environment check

If terminals behave oddly (no completion detected, notifications missing), run the doctor:
```bash
pairmux doctor
```
It reports the backend (tmux, which needs **≥ 3.2**, or the built-in multiplexer, which needs no
tmux), state-dir writability, the per-shell completion tier (`hooks`, `hooks-no-C`,
`hooks-degraded->sentinel`, or `sentinel`), and notifier availability. `hooks-no-C` means completion
works but human-interleave correlation is slightly weaker (e.g. bash 3.2, PowerShell). Fish 4+
supplies native OSC 133 marks; older/degraded Fish uses its sentinel fallback. pairmux runs natively
on macOS, Linux, and Windows 10 1809+ (built-in backend; from v1.0.0), and inside WSL. Shell
selection is `PAIRMUX_SHELL` > `SHELL` > OS fallback; on Windows `SHELL` must name a real file, else
pwsh → Windows PowerShell → COMSPEC/`cmd.exe`. Secret detection there relies on wording only.
`doctor` can return `ok:true,status:"issues"`; read the report and fixes, not just the success flag.
