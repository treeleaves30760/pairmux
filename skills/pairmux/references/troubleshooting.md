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

## A quiet command did not satisfy `wait --idle`

This is intentional. `--idle MS` first observes `MS` ms of output silence, then refreshes terminal
liveness and status. A command that is sleeping, blocked on I/O, or otherwise still running remains
`running`; pairmux keeps waiting until the shell is actually idle or the overall timeout expires.
A prompt returns `awaiting-input`, and a vanished pane returns `dead`.

For a long-lived process where silence is expected, use `peek` to inspect it or `wait --pattern` for a
future readiness line. Use `run` for finite commands because hooks return precise `done` +
`exit_code`.

This idle/prompt behavior is armed only when idle is the default condition or `--idle` is explicit.
`wait --pattern ...` and `wait --done` keep blocking at a prompt until their requested condition,
pane death, or timeout. Add `--idle MS` when either condition should race idle/prompt detection.

`wait --human` is the exception: a prompt never resolves it, with or without `--idle`. Handing off
*because* of a prompt and then being handed that prompt straight back is a spin, so `--human` waits
for the prompt to be **answered** — a human `note`, the terminal visibly moving again (`running`),
or the command completing (`done`). If it times out, the human has not come yet: the `next` repeats
the same wait with a longer deadline, and following it is the correct move.

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

A terminal created with `--cmd`, an unknown interactive shell, or a supported shell whose OSC 133
probe failed reports `mode:"sentinel"`. Completion detection still works via an injected marker.
POSIX-like shells carry the prior exit code in `$?`; Fish fallback uses `$status`, so Fish commands
remain valid. A `--cmd` terminal is a **program you drive with `send`/`peek`, not `run`**; interact
with it using `send` and observe with `peek`/`wait`/`log`.

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
on macOS, Linux, and Windows 10 1809+ (built-in backend; from v1.0.0), and inside WSL. On
Windows the default shell is PowerShell and `cmd.exe` terminals use sentinel mode; secret prompts
there are recognized by wording only.
