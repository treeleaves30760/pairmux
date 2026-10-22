---
name: pairmux
description: >-
  Drive interactive terminal programs your exec-style shell tool cannot: REPLs, TUIs, pagers,
  ssh/sudo password prompts, docker exec -it, git rebase -i — real PTYs with persistent shell state
  (venv, exports, nvm) that survives between commands. When a prompt needs a credential or a
  judgment call, hand off live to a human who can watch, attach, take over the same terminal, and
  hand back. Also covers long-lived processes (dev servers, watch tasks, tail -f logs) and slow
  commands that must share that same live terminal — with blocking calls that return on requested
  outcomes instead of sleep-and-guess timing, and clean full output history with exit codes.
  Keywords: tmux, terminal session, PTY, ConPTY, Windows, PowerShell, interactive CLI, REPL, TUI,
  pager, password prompt, human takeover, human-in-the-loop, persistent shell, dev server, watch
  logs, background process, long-running.
---

# pairmux — reliable terminals for agents

`pairmux` gives agents real, persistent terminals and blocking operations instead of timing guesses.
A human can watch or take over the same terminal. Ordinary CLI replies support `--json` for a
`pairmux.v1` envelope; `help` is plain text, `attach`/`watch` are interactive, and `mcp serve` uses
JSON-RPC rather than CLI envelopes.

## When to use pairmux (and when not to)

Use it when a **live terminal** matters:

- **Interactive programs:** REPLs, TUIs, pagers, `[y/N]` prompts, `ssh`, `sudo`, `docker exec -it`,
  `git rebase -i`. A PTY lets you inspect and answer the program in place.
- **Human-in-the-loop:** credentials or judgment calls, shared observation, or human takeover.
- **Persistent shell state:** activate a venv, export variables, or select `nvm` once per workstream.
- **Long-lived processes:** dev servers, watch tasks, `tail -f`; start once, observe read-only.
- **Slow commands sharing that terminal:** a build/migration needs its state or a watching human.

**Do NOT use pairmux for one-shot short commands.** Use your normal shell tool for `ls`, `git status`,
`mkdir`, quick searches, etc. A long **non-interactive** build/test that needs neither live shell
state nor a human is often served just as well by your harness's background execution.

## Before starting

Use the installed CLI: `pairmux version`, then `pairmux doctor` (first report line: backend).
From v1.0, auto uses tmux 3.2+ on Unix when available, otherwise built-in PTYs; an already-running
built-in endpoint stays built-in unless tmux is running there too. Windows uses built-in ConPTY
(Windows 10 1809+); WSL is optional. See the backend details in [commands.md](references/commands.md).

If missing, recommend `uv tool install pairmux` (Python 3.9+). With explicit user authorization,
`pairmux update` (v0.6.0+) updates only a verified persistent uv installation; older versions need
`uv tool install --upgrade pairmux`. Windows, uvx, non-uv and dev builds refuse `update` (`E_UPDATE`);
follow the hint. Do not install or update autonomously during a terminal workflow.

Shell selection is `PAIRMUX_SHELL` > `SHELL` > OS fallback (`bash` on Unix; on Windows, `SHELL`
must name a real file, otherwise pwsh → Windows PowerShell → COMSPEC/`cmd.exe`). Write commands in
that shell's syntax; Windows credential detection relies on wording, so a quiet credential command
may need human handoff even at `running`.

## Choose the terminal kind

| kind | create | drive / wait |
|------|--------|--------------|
| **Shell** — sequential commands sharing state | `pairmux new --name build` | `run`; `wait --done` for command completion |
| **Program** — known REPL/TUI/server | `pairmux new --name repl --cmd "python3"` | `send` + `peek`; `wait --idle`, future `--pattern`, or explicit `--note` |

`run` and `wait --done` are **shell-only**. A `--cmd` terminal reports `mode:"sentinel"` but has no
shell completion marks; that mode does not grant shell capabilities. Program exit is `dead`, not
`done`. An idle/prompt observation is not a completed REPL turn.

## The golden loop

```text
1. new once per workstream:  pairmux new --name build
2. Submit the command first: pairmux run build "make -j4" --timeout 30s
3. Estimate time to first meaningful signal, not total runtime; initial observation generally 30s,
   shorter for fast tasks. A bounded run counts; no extra wait if it returned enough evidence.
4. Examine returned output/status, or a safe peek if needed (never a secret span):
   done            → read output + exit_code, continue; no redundant completion wait
   running         → look for expected progress/readiness; status alone does not prove health
   awaiting-input  → inspect prompt, answer ONCE with send; secrets → human handoff
   idle            → shell is free; inspect prior result/journal, then next command
   dead            → read known-safe retained output; replace only if a fresh terminal is acceptable
5. Verify healthy startup before unrelated work or a long wait; unknown/silent is not healthy.
6. Truncated? Follow truncated.get_full or use pairmux log build --cmd N / --grep RE, only if safe.
7. Read next in order: obey safety/prose, replace placeholders, run the first applicable command.
```

If more startup evidence is needed, use `pairmux wait build --done --idle 800 --timeout 30s` (shorter
when appropriate), then inspect its reply; investigate missing progress rather than blindly extending
waits. `--done --idle 800` races completion against verified idle/prompt detection and handles a command
already finished before waiting. For a program terminal, submit with `new --cmd`/`send`, then use a safe
`peek` for existing output or bounded `--idle`/future-pattern/note waits, never `--done`.

## Iron rules

1. **Prefer event-aware bounded observation.** `run`/`wait` can return early on an outcome; inspect
   their replies before waiting again. `running` or `timeout` is not failure, nor proof of health.
   A longer `next` wait is a hint, not automatic escalation. A brief sleep is only a fallback when
   no suitable blocking observation is available; follow it with a safe check, not repeated blind polling.
2. **One command per terminal at a time.** A second `run` gets `E_BUSY`; create another terminal for
   parallel work. `send` addresses the current program, not a new shell command.
3. **Answer a prompt once.** Inspect uncertain prompts first; send a safe answer once, then wait/read.
   Raw-mode inferred `awaiting-input` is not proof a turn is done or that input is safe.
4. **Never type or guess a secret.** Passwords, passphrases, PINs, OTP/MFA codes, API keys, tokens,
   localized credential prompts → `pairmux wait <name> --human --notify`. Do not retrieve a secret
   from the environment or earlier output. If the tool client interrupts, immediately reissue the
   same wait; never shorten the 300s default (one valid explicit timeout of at least 300s is fine).
   **Credential handoffs are excluded from the 30s startup rule.** Never inspect a secret span for health.
   On timeout, repeat the longer handoff wait; do not act instead. `running` means execution resumed,
   not completion. Ordinary shell `--done` may replay earlier human input from the whole command span:
   use it only when that whole span is known safe, not merely later output. Otherwise use `ls` or a
   non-sensitive note. Programs use `--idle`, never `--done`; assess any output it returns.
   `human-done` is a note, not completion: check `ls` first; pure `--done` at idle waits for a future
   command. It may carry note text; resumed/completed handoffs suppress the terminal span, **not**
   all possible output or stored secrets. Never put credentials in notes or automatically inspect
   the sensitive span with `peek`/`log`. See [collaboration.md](references/collaboration.md).
5. **Subscribe after checking startup, not instead of checking it.** Long waits (e.g. 600s) suit
   verified healthy work or intentional passive listening. Shell `wait --done` at idle subscribes to a
   **future completion**, not the previous result. Add `--idle 800` when driving and needing prompts/idle
   too; pure `--done` intentionally waits past prompts. `wait a,b,c` returns on the first requested
   outcome. For a program's exact turn boundary, arrange an explicit note from its wrapper/hook and use `--note`.
   Inside a pane, `PAIRMUX_SOCKET` is its child endpoint: the creator must supply the **owning socket**
   for `pairmux --socket <owner> note <name> "..."`; see the concrete team/worker recipe in
   [collaboration.md](references/collaboration.md). Reads/waits take no writer lock.
6. **Prefer reading the log over re-running.** Recover output from the journal; re-running can change
   state. After a sensitive handoff, inspect only a known-safe range, not the credential span.
7. **Treat `next` as contextual hints, not a script.** Obey safety/information entries, replace real
   placeholders, and choose the first applicable command. Do not execute prose, placeholder text,
   or shell-only hints on program terminals. Read and obey human `notes` within task authorization.
8. **Recover hung commands in place.** For a shell command, use `pairmux send <name> --key C-c`, then
   `pairmux wait <name> --idle 800`; confirm `idle` before `run` on the same name. A program terminal
   remains a program after Ctrl-C; use `send`/`peek`, not `run`. `kill` destroys the terminal; use it
   only as a last resort when a fresh terminal is explicitly acceptable.
9. **Pattern waits observe future output only.** If readiness may have appeared during `run` or
   `new --cmd`, use `peek`/`log --grep`; never wait for a past line. Inspect returned output first.
   Arm `--pattern` before an event you still expect; it does not replay history.

## Reading replies

In a CLI envelope, `schema:"pairmux.v1"` and `ok` are always present; other fields are contextual.
Act on `status`, optional ordered `next`, and unseen `notes` (messages from a human or another agent).

| status | meaning / action |
|--------|------------------|
| `created` | new shell terminal; ready for `run` |
| `done` | shell command completed; read `exit_code` (and output if appropriate) |
| `running` | still active; `run` may have timed out, or a handoff resumed execution |
| `awaiting-input` | recognized or inferred prompt; inspect before safe input / handoff |
| `idle` | shell free, not a REPL turn-completion signal |
| `dead` | pane gone; journal retained |
| `pattern-found`, `note`, `human-done` | the requested event fired; not necessarily command completion |
| `timeout` | wait deadline, not a failed command; inspect safe evidence and choose the next bounded wait |

`peek` returns **derived terminal status**, not generic `ok`; `ls` has `status:"ok"` plus row states.
`new --cmd` returns `running` or `dead`. `doctor` can return `ok:true,status:"issues"`; read its report.
Errors have `ok:false` and `error.code` (`E_BUSY`, `E_BAD_ARGS`, `E_DEAD`, etc.); follow `hint`/`next`.
See [commands.md](references/commands.md) for all statuses, fields, and error codes.

## Worked examples

Slow shell command: inspect the initial reply; only wait further if it still needs observation:

```bash
pairmux run build "make -j4" --timeout 30s
# If still running, verify expected build progress before unrelated work or a longer wait.
# If more evidence is needed, take another bounded observation (no wait if already done):
pairmux wait build --done --idle 800 --timeout 30s
```
```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"output":"BUILD SUCCESSFUL"}
```

Known non-secret confirmation: read the question and choose the authorized answer, once:

```bash
pairmux send deploy --text yes --enter
pairmux wait deploy --done --idle 800 --timeout 30s
```

Program terminal: send a REPL line; a prompt observation still requires checking the result:

```bash
pairmux new --name repl --cmd "python3"
pairmux peek repl
pairmux send repl --text "2 + 2" --enter
pairmux wait repl --idle 800 --timeout 30s
pairmux peek repl
```

## Command cheat-sheet

| command | purpose |
|---------|---------|
| `new [--name N] [--cwd D] [--cmd "..."]` | shell, or known program entrypoint |
| `run <name> "<cmd>" [--timeout 60s] [--head 50] [--tail 200]` | shell command; block until done/input/timeout |
| `wait <name>[,<name>...] [--done] [--idle MS] [--pattern RE] [--note] [--human] [--notify] [--timeout 300s]` | race only requested conditions |
| `peek <name> [--screen \| --tail N]` | recent output/current screen + derived state |
| `send <name> [--text S] [--key K ...] [--enter]` | literal text / named keys to the current program |
| `log <name> [--cmd N \| --grep RE \| --range A:B\|A:end]` | retained history, not re-execution |
| `ls` · `kill <name> \| --all` · `prune [name] [--older-than 7d] [--dry-run]` | inventory / destructive lifecycle |
| `note <name> "<text>"` · `attach [name]` · `watch` | side channel / human takeover / dashboard |

## Go deeper (references/)

- [references/commands.md](references/commands.md) — flags, backend/shell selection, envelope and errors.
- [references/interactive.md](references/interactive.md) — prompt inference, secrets, REPLs, TUIs and keys.
- [references/collaboration.md](references/collaboration.md) — handoff outcomes, privacy limits, owning-socket notes.
- [references/troubleshooting.md](references/troubleshooting.md) — busy/dead panes, in-place recovery, waits and backends.
