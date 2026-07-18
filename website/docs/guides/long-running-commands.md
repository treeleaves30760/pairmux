---
title: Long-running commands & timeouts
description: The run → wait → peek loop for builds, test suites, and anything slow.
---

# Long-running commands & timeouts

The golden rule: **never `sleep` to guess how long a command takes.** `pairmux run` returns when the command finishes, needs input, or reaches its timeout. When a command outlives that timeout, `run` hands you a structured way to keep waiting.

## The pattern: run → wait → peek

```bash
# 1. Start the command. run blocks up to --timeout.
pairmux run build "make -j4" --timeout 30s
```

If it finished in time, you get the result directly:

```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"hello world"}
```

If its deadline arrives without completion or a recognized prompt, `run` returns `status: running` (an outcome, not an error) with the current tail and the next steps to take:

```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"build","mode":"hooks","next":["pairmux peek build","pairmux log build --cmd 1"]}
```

```bash
# 2. Keep waiting until the shell is truly idle.
pairmux wait build --idle 800
```

```json
{"schema":"pairmux.v1","ok":true,"status":"idle","terminal":"build","mode":"hooks","next":["pairmux peek build","pairmux run build \"...\""]}
```

```bash
# 3. Look whenever you want — peek is read-only and never blocks.
pairmux peek build
```

## Choosing a timeout

`--timeout` is a Go duration (`30s`, `2m`, `1h`). Pick a value that bounds how long you're willing to *block*, not how long the command will take — a timeout just returns control to you. A reasonable loop is a short first `run`, then `wait` in larger increments.

## Waiting for a specific signal

Output silence alone does not count as idle: a sleeping or I/O-blocked command remains `running`, and
the wait continues to its timeout. For a command whose *completion* is not the interesting event — a
dev server that never exits, say — wait for a future pattern instead of idleness:

```bash
pairmux run web "npm run dev" --timeout 3s     # returns running almost immediately
pairmux wait web --pattern "listening on"       # block until that line appears
```

```json
{"schema":"pairmux.v1","ok":true,"status":"pattern-found","terminal":"dev","mode":"hooks","output":"\nnow listening on port 3000","next":["pairmux peek dev"]}
```

`--pattern` is an RE2 regex and is matched against shaped output (ANSI stripped), so `error|panic|listening` all work.

## Finding one line in a huge log

When output is truncated, the reply tells you how to get the rest — you never have to re-run the command:

```json
"truncated":{"omitted_lines":1187,"get_full":"pairmux log build --cmd 17"}
```

Then query the complete selected history instead of re-running:

```bash
pairmux log build --cmd 17            # the whole command's output
pairmux log build --grep "error|FAIL" # every match, with journal line numbers
pairmux log build --range 400:460     # a specific line range
pairmux log build --range 1:end       # every shaped journal line
```

Routine observation remains bounded so a huge journal cannot flood an agent: `peek` reads at most the
final 64 KiB, and default `log` reads at most the final 4 MiB before keeping 500 lines. Those responses
report a skipped raw prefix as `truncated.omitted_bytes` and point to the executable
`pairmux log NAME --range 1:end` recovery. Explicit `--cmd`, `--grep`, and `--range` selections read
the complete requested history and may therefore return large replies.

## Tips

- **Prefer reading the log over re-running.** The journal already has the complete output; re-running wastes time and can change state.
- **Tune `--head`/`--tail`** on `run` if you want more or less inline output (defaults 50 / 200).
- **One command per terminal.** Starting a second command while one is running returns `E_BUSY`. Open another terminal with `pairmux new` for parallel work.
