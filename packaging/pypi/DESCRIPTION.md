# pairmux

**Let AI agents drive interactive terminal programs — and hand off to a human when they can't.**

[Homepage](https://pairmux.treeleaves30760.com) ·
[Documentation](https://pairmux-docs.treeleaves30760.com/) ·
[CLI reference](https://pairmux-docs.treeleaves30760.com/cli-reference) ·
[Changelog](https://github.com/treeleaves30760/pairmux/blob/main/ChangeLog.md) ·
[Source](https://github.com/treeleaves30760/pairmux)

pairmux is a small Agent-Computer Interface layer over tmux with no separate service to supervise.
Agents get a real PTY with persistent shell state and actionable command outcomes; humans keep
normal access to the same live terminal — watch, take over, hand back.

## Why pairmux

- **A real PTY.** Exec-style agent shell tools run commands without a terminal, so REPLs, `psql`,
  `ssh` password prompts, `docker exec -it`, `git rebase -i`, pagers, and TUIs are impossible
  there. pairmux makes them drivable.
- **Human handoff on credentials and judgment calls.** A recognized secret prompt never suggests
  an answer; `wait --human --notify`, `attach`, and `note` turn a blocked credential prompt into a
  resumable checkpoint — and the wait ends as soon as the human answers, note or no note.
- **Persistent shell state.** `source venv/bin/activate`, `export`, and `nvm use` happen once in a
  live shell instead of being re-composed into every command.
- **Actionable waits and captured history.** `run` blocks until completion, a recognized prompt,
  or timeout (never killing the command); journals retain full output with exit codes, and
  `--json` emits a versioned `pairmux.v1` envelope with ordered next steps.

## Install

With [uv](https://docs.astral.sh/uv/getting-started/installation/) installed, add a persistent
`pairmux` command to PATH:

```bash
uv tool install pairmux
pairmux version
pairmux doctor
```

This ordinary install retains your environment and uv configuration, including index overrides,
and may reuse cache. Check `command -v pairmux` for an older installation shadowing the new one.

### Update (v0.6.0+)

From **v0.6.0** onward, update the currently running, verified persistent uv tool installation
(including one created by the installer):

```bash
pairmux update
```

**v0.5.3 and older have no `update` command.** First use `uv tool install --upgrade pairmux` with
your normal configuration to obtain v0.6.0+ when available, then use the new command.

The updater needs uv installed. It clears all inherited `UV_*` settings, ignores uv configuration,
and reinstalls from **`https://pypi.org/simple`** with no persistent cache or source builds,
replacing old exact pins, constraints, indexes, and extras. It selects the latest compatible stable
wheel, never below the running version's numeric core; recognized alpha/beta/rc builds can advance
to that core's final or a later stable release, but dev/snapshot builds are refused.

A version change returns `updated`; a same-version reinstall returns `refreshed`, not a no-op.
Failures return `E_UPDATE` with a recovery hint, without a guaranteed rollback. Recover with the
original manager, or `uv tool install --upgrade pairmux` for uv installs (normal configuration
applies). It does not update Homebrew, RPM, pipx/pip, manual/development installs, or temporary uvx
runs, and does not touch tmux. There are no command-specific options, automatic updates, sudo, or
uv bootstrap/update; global `--json` is supported.

### Optional temporary run with uvx

```bash
uvx pairmux version
uvx pairmux doctor
```

`uvx` remains supported. It uses a temporary tool environment, which uv may cache, or reuses an
existing uv-managed installation; it does not promise the latest version or a fresh download every
time. Temporary runs cannot use `pairmux update`.

### Alternative installer

The [inspectable installer](https://pairmux.treeleaves30760.com/install.sh) persistently installs
pairmux with uv from the public PyPI index:

```bash
curl -fsSL https://pairmux.treeleaves30760.com/install.sh | bash
```

To review it before execution, download successfully first, inspect the file, then run it:

```bash
installer_dir=$(mktemp -d) &&
curl -fsSL https://pairmux.treeleaves30760.com/install.sh -o "$installer_dir/install.sh" &&
less "$installer_dir/install.sh" &&
bash "$installer_dir/install.sh"
```

The one-line pipe executes downloaded code; it is not a substitute for that review. The installer
clears all inherited `UV_*` settings, ignores uv configuration, and uses **`https://pypi.org/simple`**
with no persistent cache and wheels only. Normal uv commands use your regular configuration. See
[installation guidance](https://pairmux-docs.treeleaves30760.com/#install)
for PATH setup and other supported methods (`pipx install pairmux` or `python -m pip install pairmux`
in a dedicated environment).

### Requirements

The [PyPI pairmux wheels](https://pypi.org/project/pairmux/) contain a prebuilt native Go binary,
so installation needs no Go toolchain or source build. Wheel installers require Python 3.9 or newer;
the installed `pairmux` executable itself contains no Python code. If needed, the installer obtains
uv from [Astral's official installer](https://docs.astral.sh/uv/getting-started/installation/), and
uv may obtain managed Python from Astral's upstream Python distributions. **Only the pairmux wheel
comes from PyPI in that installer flow; uv/Python may come from Astral upstream.**

PyPI package requirements and wheel targets:

- **tmux 3.2 or newer, installed separately** with your system package manager; neither the wheel
  nor the installer supplies tmux or runs sudo to install it
- macOS 12+ or manylinux_2_17 (glibc 2.17+)
- x86-64 or ARM64 (aarch64)
- no native Windows wheel; use a compatible Linux distribution inside WSL

The quickstart below assumes the persistent `pairmux` command is on PATH. For temporary use,
prefix each command with `uvx`.

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
exist. See the [Agent Skills guide](https://pairmux-docs.treeleaves30760.com/skills) for all
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
