# pairmux

**Blocking, observable tmux terminals for AI agents — with live human handoff.**

[Documentation](https://treeleaves30760.github.io/pairmux/) ·
[CLI reference](https://treeleaves30760.github.io/pairmux/cli-reference) ·
[Changelog](https://github.com/treeleaves30760/pairmux/blob/main/ChangeLog.md) ·
[Source](https://github.com/treeleaves30760/pairmux)

pairmux is a small coordination layer over tmux with no separate pairmux service to supervise.
Agents get actionable command outcomes and clean, bounded-by-default output; humans keep normal
access to the same live terminal.

## Why pairmux

- **Actionable waits.** `run` blocks until a command completes, a recognized prompt appears, or
  its timeout expires. Completed runs include the exit code and duration; if no completion was
  observed by the deadline, the response reports `running` and does not kill the command.
- **Captured history.** Managed terminals write pane output to a per-terminal journal. Routine
  reads stay bounded, while explicit `log` selectors retrieve a captured command or selected
  journal history. Truncated replies point to the command that fetches the rest.
- **Shared control.** Humans can `watch`, `attach`, and leave a `note`. Read-only observation is
  lock-free; only one `run` writer is allowed per terminal, and a conflict returns `E_BUSY`.
- **Agent-readable replies.** `--json` emits a versioned `pairmux.v1` envelope with status, shaped
  output, recovery hints, and ordered next steps.

## Install

```bash
uv tool install pairmux
# or
pipx install pairmux
# or, inside a dedicated environment
python -m pip install pairmux
```

The wheel contains a prebuilt native Go binary, so installation needs no Go toolchain or source
build. Wheel installers must select Python 3.9 or newer; the installed `pairmux` executable itself
contains no Python code.

PyPI package requirements and wheel targets:

- tmux 3.2 or newer
- macOS 12+ or manylinux_2_17 (glibc 2.17+)
- x86-64 or ARM64 (aarch64)
- no native Windows wheel; use a compatible Linux distribution inside WSL

Check the local environment after installation:

```bash
pairmux doctor
```

## 60-second quickstart

Create one managed terminal, then run commands in it:

```bash
pairmux --json new --name demo
pairmux --json run demo "echo hello from pairmux"

# A timeout returns status=running; it does not kill the command.
pairmux --json run demo "sleep 2; echo finished" --timeout 1s

# When status is running, keep waiting without sleep-and-guess polling.
pairmux --json wait demo --idle 800
pairmux --json peek demo
```

`wait --idle` refreshes terminal state after output becomes quiet; silence alone is not treated as
completion. `peek` and `log` are read-only, so other agents can inspect the same terminal without
taking its writer lock.

## Teach your agent

pairmux embeds its Agent Skill, including the `new → run → wait/send/log` loop and safe human
handoff guidance:

```bash
pairmux skill install --target codex --dry-run
pairmux skill install --target codex
```

Use `--target all` to update only the supported agent configuration directories that already
exist. See the [Agent Skills guide](https://treeleaves30760.github.io/pairmux/skills) for all
targets and install locations.

MCP clients can launch the built-in stdio server and use the core terminal operations as typed
tools:

```json
{
  "command": "pairmux",
  "args": ["mcp", "serve"]
}
```

## Interactive work and human handoff

After 800 ms of output quiet, pairmux recognizes supported prompt patterns: confirmations,
password/passphrase/passcode prompts, pager markers, press-key messages, and Python's `>>>`. It
reports `awaiting-input` and never auto-answers. Replace the uppercase placeholders below before
sending ordinary input:

```bash
pairmux send NAME --text VALUE --enter
```

For a secret-shaped prompt, the response recommends `pairmux wait NAME --human --notify`. A human
can enter the same live pane with `pairmux attach NAME`, provide the input, and detach with
the configured tmux binding (default `Ctrl-b d`). Running `pairmux note NAME "ready"` afterward
records a message and releases an agent waiting with `--human`; it does not itself detach or enforce
control ownership. This avoids routing the input through the agent-facing `send` command. Prompt
recognition is heuristic, desktop notification is best-effort, and whether an application echoes or
records input remains application-dependent.

## How it works

pairmux does not implement another PTY or add a background service of its own. tmux remains the
terminal-state engine; short-lived pairmux commands add completion detection, captured output,
model-friendly shaping, and coordination around the live pane. `pairmux attach` starts a native
tmux client against the correct managed session whenever a human needs to observe or take over.

pairmux is open source under the [MIT License](https://github.com/treeleaves30760/pairmux/blob/main/LICENSE).
