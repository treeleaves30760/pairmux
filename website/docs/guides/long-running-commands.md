---
title: Long-running commands & timeouts
description: The run → wait → peek loop for builds, test suites, and anything slow.
---

# Long-running commands & timeouts

The golden rule: **never use `sleep` outside the managed terminal to guess how long a command
takes.** `pairmux run` returns when the command finishes, reaches a recognized quiet prompt, or hits
its timeout. When no completion is observed by that deadline, `run` does not kill the command; it
returns `status: running` and a structured way to continue.

## The pattern: run → wait → peek

```bash
# 1. Create the terminal once, then run commands in it.
pairmux --json new --name build
pairmux --json run build "echo hello world"
```

If it finished in time, you get the result directly:

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"hello world"}
```

Now start a deterministic slow demo. The `sleep` calls are part of the managed program; the agent is
not sleeping to poll it. `run` blocks for only one second:

```bash
pairmux --json run build 'for step in 1 2 3; do echo "step $step"; sleep 1; done' --timeout 1s
```

If its deadline arrives without completion or a recognized prompt, `run` returns `status: running`
(an outcome, not an error) with the current tail and next steps. Timing can change which step is in
the tail; this is a representative hooks-mode result:

```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"build","mode":"hooks","output":"step 1","next":["pairmux peek build","pairmux log build --cmd 2"]}
```

```bash
# 2. Wait for the next actionable terminal state.
pairmux --json wait build --idle 800
```

```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","next":["pairmux peek build","pairmux run build \"...\""]}
```

```bash
# 3. Look whenever you want — peek is read-only and never blocks.
pairmux --json peek build
```

The example reaches `idle`, but `wait --idle` can also return `awaiting-input`, `dead`, or `timeout`.
Read the returned `status` and follow its `next` field.

## Choosing a timeout

`--timeout` is a Go duration (`30s`, `2m`, `1h`). Pick a value that bounds how long you're willing to
*block*, not how long the command may run. A `run` timeout returns `status: running`; a `wait` timeout
returns `status: timeout`. Neither one kills the managed command. A reasonable loop is a short first
`run`, followed by `wait` calls with deliberate deadlines.

## Waiting for a specific signal

Output silence alone does not count as idle: a sleeping or I/O-blocked command remains `running`, and
the wait continues to its timeout. For a command whose *completion* is not the interesting event — a
dev server that never exits, say — a future pattern can be the useful outcome.

`wait --pattern` deliberately starts at the journal offset where that wait begins; it does **not**
search earlier output. This demo delays its readiness line long enough to arm the waiter:

```bash
pairmux --json new --name web
pairmux --json run web 'sleep 5; printf "listening on %s\n" "127.0.0.1:3000"; while :; do sleep 60; done' --timeout 1s
pairmux --json wait web --pattern "listening on 127" --timeout 10s
```

```json
{"schema":"pairmux.v1","ok":true,"status":"pattern-found","terminal":"web","mode":"hooks","output":"listening on 127.0.0.1:3000","next":["pairmux peek web"]}
```

For a real service, first inspect the `run` response and search existing history:

```bash
pairmux --json log web --grep "listening on 127"
```

The command text intentionally keeps the full readiness phrase out of the shell's echoed command
line. Otherwise a journal search can match the command itself before the service emits its real
readiness output.

If history has no match, arm `wait --pattern` for a future occurrence. A line can arrive between a
history read and a later wait; strict gap-free monitoring requires another agent or process to start
the waiter before the action that emits the line. After a wait timeout, search the complete log again.

`--pattern` is an RE2 regex matched against shaped output (ANSI stripped), so
`error|panic|listening` all work. Clean up the persistent demo when finished:

```bash
pairmux --json kill web
```

## Finding one line in a huge log

When output is truncated, the reply tells you how to get the rest — you never have to re-run the command:

```json
"truncated":{"omitted_lines":1187,"get_full":"pairmux log build --cmd 17"}
```

Then query the complete selected history instead of re-running:

```bash
pairmux --json log build --cmd 17            # the whole command's output
pairmux --json log build --grep "error|FAIL" # every match, with journal line numbers
pairmux --json log build --range 400:460     # a specific line range
pairmux --json log build --range 1:end       # every shaped journal line
```

Routine observation remains bounded so a huge journal cannot flood an agent: `peek` reads at most the
final 64 KiB, and default `log` reads at most the final 4 MiB before keeping 500 lines. Those responses
report a skipped raw prefix as `truncated.omitted_bytes` and point to the executable
`pairmux log NAME --range 1:end` recovery. Explicit `--cmd`, `--grep`, and `--range` selections read
the complete requested history and may therefore return large replies.

## Tips

- **Prefer reading the log over re-running.** The journal already has the command's captured output;
  re-running wastes time and can change state.
- **Tune `--head`/`--tail`** on `run` if you want more or less inline output (defaults 50 / 200).
- **One command per terminal.** Starting a second command while one is running returns `E_BUSY`. Open another terminal with `pairmux new` for parallel work.
