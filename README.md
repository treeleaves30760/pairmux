# pairmux

**Let AI agents drive interactive terminal programs — and hand off to a human when they can't.**

[PyPI](https://pypi.org/project/pairmux/) ·
[Documentation](https://treeleaves30760.github.io/pairmux/) ·
[CLI reference](https://treeleaves30760.github.io/pairmux/cli-reference) ·
[Changelog](https://github.com/treeleaves30760/pairmux/blob/main/ChangeLog.md) ·
[Source](https://github.com/treeleaves30760/pairmux)

pairmux is a small Agent-Computer Interface layer over tmux. Agents get a real PTY with persistent
shell state, blocking command outcomes, retained history, and machine-readable recovery hints.
Humans keep normal access to the same live terminal: watch the agent work, take over an interactive
prompt, hand back. Requires **tmux ≥ 3.2** at runtime (`brew install tmux` / `apt install tmux`).

## Why pairmux

- **A real PTY.** Exec-style agent shell tools run commands without a terminal, so `python` and
  `node` REPLs, `psql`, `ssh` password prompts, `npm init`, `gh auth login`, `docker exec -it`,
  `git rebase -i`, pagers, and TUIs are not merely awkward there — they are impossible. pairmux
  makes them drivable. This is a 0-to-1 capability, not an efficiency gain.
- **Human handoff on credentials and judgment calls.** A recognized secret prompt never suggests
  `send`; `wait --human --notify`, `attach`, and `note` turn "the agent hit a password prompt and
  failed" into a resumable checkpoint. The wait ends the moment the human answers — a note is how
  they say *what* they did, not a precondition for the agent to resume.
- **Persistent shell state.** `source venv/bin/activate`, `conda activate`, `export`, `nvm use`
  happen once in a live shell instead of being re-composed into every command.
- **Shared observation.** A human can attach and watch the agent drive the same live pane — and
  take over mid-command. Only one `run` writer is allowed per terminal; a conflicting run returns
  `E_BUSY`, and reads are lock-free.
- **Actionable waits.** `run` blocks until a command completes, a recognized prompt appears, or
  its timeout expires. A completed run includes its exit code and duration; if no completion was
  observed by the deadline, the response reports `running` and does not kill the command.
- **Captured history, agent-readable replies.** Journals retain everything; routine reads stay
  bounded, explicit `log` selectors retrieve full history, and `--json` emits a versioned
  `pairmux.v1` envelope with status, shaped output, recovery hints, and ordered next steps.

## When to use it — and when not to

| Workload | Reach for |
| --- | --- |
| Short one-shot commands (`ls`, `git status`, a quick `grep`) | your agent's built-in shell tool |
| Long **non-interactive** command in a fresh env, no human needed | your harness's background execution |
| Interactive programs (REPL, TUI, pager, `[y/N]`, `ssh`, `sudo`) | **pairmux** |
| Shell state that must persist across commands (venv, exports) | **pairmux** |
| Credential prompts, judgment calls, live human takeover | **pairmux** |
| A dev server or `tail -f` that a human may also watch | **pairmux** |

The first two rows are deliberate: modern agent harnesses already run long non-interactive
commands well. pairmux earns its place where a terminal, its state, or a human is part of the
workload.

## Install

pairmux requires **tmux 3.2 or newer** at runtime.

Release artifacts target:

| Platform | Architectures | Additional requirement |
| --- | --- | --- |
| macOS 12+ | x86-64, ARM64 | None for the installed binary |
| Linux | x86-64, ARM64/aarch64 | PyPI wheels use the `manylinux_2_17` / glibc 2.17+ tags |
| Windows | No native artifact | Use a compatible Linux distribution inside WSL |

### Homebrew (macOS and Linuxbrew)

The tap cask installs pairmux **and its tmux dependency** in one command, and strips the
quarantine attribute so the binary runs without a Gatekeeper prompt:

```bash
brew install --cask treeleaves30760/pairmux/pairmux
```

Available from v0.2.0; the cask is republished automatically with every stable release.

### PyPI

Install the platform wheel with an isolated tool manager, or use `pip` inside a dedicated virtual
environment:

```bash
uv tool install pairmux
# or: pipx install pairmux
# or, inside a dedicated environment:
python -m pip install pairmux
```

Wheel installers must select Python 3.9 or newer. The wheel contains a prebuilt native Go binary;
the installed `pairmux` executable contains no Python code and needs no Go toolchain.

### Checksummed release archive

The installer selects the latest matching GitHub release archive, verifies its SHA-256 checksum,
and installs the binary under `~/.local/bin` by default:

```bash
curl -fsSL https://raw.githubusercontent.com/treeleaves30760/pairmux/main/install.sh | sh
```

Use `PAIRMUX_INSTALL_DIR` to choose another directory. Specific versions are available from
[GitHub Releases](https://github.com/treeleaves30760/pairmux/releases).

### Direct `.deb` and `.rpm` packages

Each stable GitHub release includes `.deb` and `.rpm` files for Linux x86-64 and ARM64. Download the
matching file and `checksums.txt` from
[GitHub Releases](https://github.com/treeleaves30760/pairmux/releases), verify its SHA-256 checksum,
then install that local file (the [Getting Started guide](https://treeleaves30760.github.io/pairmux/)
shows the complete commands):

```bash
sudo apt install ./downloaded-file.deb
# or, on an RPM-based distribution
sudo dnf install ./downloaded-file.rpm
```

### APT repository (Debian/Ubuntu)

A signed APT repository is published from every stable release, so `apt install pairmux`, upgrades,
and older-version pins work without downloading files by hand, and the package declares its
`tmux (>= 3.2)` dependency. Enroll the key and source using the verification snippet in the
[pairmux-apt repository](https://github.com/treeleaves30760/pairmux-apt#install), then:

```bash
sudo apt update && sudo apt install pairmux
```

### Build this checkout

Source builds require Go 1.25:

```bash
make build
mkdir -p "$HOME/.local/bin"
install -m 0755 bin/pairmux "$HOME/.local/bin/pairmux"
pairmux version
```

After installation, check tmux, state access, shell integration, and notification support. The live
shell probe uses an isolated temporary tmux server and does not disturb managed terminals:

```bash
pairmux doctor
```

## Reproducible quickstart

The following sequence uses only standard macOS/Linux shell commands. Use a different valid name if
`demo` is already an active pairmux terminal.

```bash
# 1. Create the managed terminal before using it.
pairmux --json new --name demo

# 2. Run a command and wait for completion.
pairmux --json run demo "echo hello from pairmux"

# 3. Demonstrate a timeout without killing the command.
#    This returns status=running after one second.
pairmux --json run demo "sleep 2; echo finished" --timeout 1s

# 4. Keep waiting without sleep-and-guess polling.
#    Silence alone is not completion; pairmux refreshes the terminal state.
pairmux --json wait demo --idle 800 --timeout 10s

# 5. Inspect recent output, then retrieve the complete second command.
pairmux --json peek demo
pairmux --json log demo --cmd 2

# 6. Stop the terminal when finished. Its journal is retained.
pairmux --json kill demo
```

`run` returns `done`, `running`, or `awaiting-input`. A default or explicit idle wait can
return `idle`, `awaiting-input`, `dead`, or `timeout`; it does not mistake a quiet running
program for a completed command. `peek`, `log`, `ls`, and `wait` do not take the terminal's
`run` writer lock.

## Interactive work and human handoff

After 800 ms of output quiet, pairmux can recognize supported prompt shapes: confirmations,
secret prompts (password/passphrase/passcode, PIN, OTP/MFA/verification codes, API keys, and the
standard sudo password translations for zh/de/fr/ja/es/pt/ko/ru), pager markers, press-key
messages, and Python's `>>>`. pairmux never auto-answers, and a secret-shaped response recommends
human handoff instead of suggesting `send`.

**Recognition is best-effort and biased toward English plus those common locales.** A prompt the
patterns miss — an unusual wording, another locale, or a full-screen dialog like pinentry — leaves
the terminal `running` (a false negative, never an auto-answer). If a command you know needs
credentials sits quiet at `running`, treat that as a handoff candidate: `peek --screen`, then
`wait --human --notify`. Set `PAIRMUX_SECRET_PROMPT_RE` to an RE2 pattern to extend (never
replace) the builtin secret recognition for your environment; `pairmux doctor` validates it.

This safe demo creates its terminal first and uses a disposable input value. The simulated program
turns terminal echo off before reading:

```bash
# Terminal A: create a terminal and start the prompt.
pairmux --json new --name handoff
pairmux --json run handoff "sh -c 'printf \"Password: \"; stty -echo; IFS= read -r secret; stty echo; printf \"\\ninput received\\n\"'"

# Once run reports awaiting-input, hand off and block.
pairmux --json wait handoff --human --notify --timeout 5m
```

From a second interactive terminal, outside an existing tmux client:

```bash
pairmux attach handoff
```

Type a disposable value such as `demo-only`, press Enter, then detach using the configured tmux
binding (default `Ctrl-b d`). Back in the second shell, leave the hand-back note:

```bash
pairmux --json note handoff "demo input completed"
```

The wait in Terminal A then returns `human-done` with that text. Had you skipped the note, the wait
would still have returned — `--human` also ends the moment the prompt is answered and the terminal
is moving again (`status: running`, with `wait --done` offered for following the rest), or when the
command finishes outright (`status: done` with its `exit_code`). Neither carries `output`: the span
they would quote is the span you typed into. If nobody answers, the wait times out with a `next`
that repeats the same wait at a longer deadline rather than inviting the agent to act. Clean up
afterward with:

```bash
pairmux --json kill handoff
```

`attach` needs a TTY and deliberately refuses to nest inside tmux. If you are already in tmux,
detach to the outer shell first or use another terminal, then run `pairmux attach`. A tmux
`switch-client` cannot cross from another server to pairmux's named socket. `note` does not detach a
client or enforce exclusive control; it records a coordination event that releases `wait --human`
and tells the agent *what* you did, which a bare answer in the pane cannot.

`--notify` is best-effort and needs `osascript` on macOS or `notify-send` on Linux. Human
handoff avoids routing input through the agent-facing `pairmux send` command, but pairmux cannot
guarantee how another application echoes, stores, or logs its input. Never type a real secret into a
demo, and never ask an agent to guess one.

## The `pairmux.v1` envelope

Ordinary non-interactive command replies have a friendly text form by default. Add `--json`
before or after the command for a compact, one-line `pairmux.v1` envelope:

```text
pairmux [--json] [--socket NAME] <command> [args]
```

Use a literal `--` when the command being sent contains a token such as `--json` or `--socket`.
`attach` and `watch` are interactive human interfaces, `help` is plain help text, and
`mcp serve` reserves stdout for JSON-RPC; those interfaces do not produce ordinary CLI envelopes.

Common terminal states:

| State | Meaning |
| --- | --- |
| `idle` | The shell is at a prompt with no pairmux-recorded command running |
| `running` | A command or program is executing |
| `awaiting-input` | A running command is quiet and its last line matches a supported prompt |
| `unknown` | The terminal is alive but recent activity or unreadable state prevents a safe classification |
| `dead` | The tmux pane is gone; its journal remains available |

`run` reports `done`, `running`, or `awaiting-input`; `wait` can also report `idle`,
`pattern-found`, `human-done`, `dead`, or `timeout`. See the
[CLI reference](https://treeleaves30760.github.io/pairmux/cli-reference) for every command status,
field, flag, and exit behavior.

Errors set `ok:false` and include a stable `error.code`: `E_NO_TERMINAL`, `E_EXISTS`,
`E_BUSY`, `E_DEAD`, `E_BAD_ARGS`, `E_TMUX`, or `E_INTERNAL`. Actionable replies carry an
ordered `next` array; safety or information entries may precede the first executable command.

## Completion modes

`new` records either `hooks` (OSC 133 marks injected for bash/zsh or emitted by fish 4+) or
`sentinel` (a shell-specific marker carrying the previous exit status). If a nominal hooks shell
emits no ready mark, `new` records `sentinel`. `doctor` may call bash 3.2 `hooks-no-C`; its stored
terminal mode is still `hooks`.

Program terminals created with `new --cmd` also report `sentinel` to keep the envelope's mode field
two-valued, but they do not accept `run` or receive per-command sentinel markers. Drive them with
`send`/`wait`/`peek`; their status follows the program pane's liveness and recognized prompts.

## State and retained history

The state root is `$PAIRMUX_STATE_DIR`, `$XDG_STATE_HOME/pairmux`, or
`~/.local/state/pairmux`. Each tmux endpoint gets an isolated `<root>/.sockets/<sha256>/` namespace,
so equal terminal names on different endpoints do not share metadata. Historical default-endpoint
state remains readable and is never moved implicitly.

`peek` and unqualified `log` are bounded. A truncated reply provides `truncated.get_full`, such as
`pairmux log demo --range 1:end`. Explicit `log --cmd`, `--grep`, and `--range` selectors scan the
complete selected history and may return large results.

`peek`, `log`, `ls`, and `wait` record no journal events. `attach` starts a native tmux
client; a human records the hand-back explicitly with `pairmux note`.

Retention has a documented exit: `kill` keeps a terminal's journal so post-mortem `log` still
works, and `pairmux prune [name] [--older-than 7d] [--dry-run]` reclaims the disk once the history
is no longer needed — dead terminals' directories and `.prev` rotation archives, never a live
terminal's journal. `pairmux doctor` reports how much the state root currently retains.

## Teach an AI agent

pairmux embeds its Agent Skill. The skill teaches the `new → run → wait/send/log` loop and the
safety rules: do not guess timing with `sleep`, do not send secrets, and inspect retained output
before re-running work.

Preview or install one target, or update supported agent directories that already exist:

```bash
pairmux skill install --target codex --dry-run
pairmux skill install --target codex
pairmux skill install --target all
```

Supported targets and install paths are documented in the
[Agent Skills guide](https://treeleaves30760.github.io/pairmux/skills). The companion
[`pairmux-skills` repository](https://github.com/treeleaves30760/pairmux-skills) is the public
source of the embedded skill.

## MCP clients

`pairmux mcp serve` exposes core operations as typed tools over the MCP `2025-11-25` stdio
transport. Point a client at the binary directly (the surrounding configuration key varies):

```json
{
  "command": "pairmux",
  "args": ["mcp", "serve"]
}
```

The server advertises typed tools for `new`, `run`, `peek`, `wait`, `send`, `log`, `ls`, `kill`,
`note`, and `doctor`. Tools invoke the executable with argv arrays, never a wrapper shell.

A CLI tool result, including a pairmux command error, returns its envelope as both
`structuredContent` and JSON text. MCP-level argument, transport, and capture-limit errors do not.
Subprocess capture is limited to 1 MiB stdout and 64 KiB stderr; overflow terminates it. Narrow large
`pairmux_log` calls with `command_id`, `grep`, or a smaller `range`.

Clients should require confirmation before running commands, sending input, or killing terminals
when those actions can have side effects.

## Documentation

- [Getting started and task guides](https://treeleaves30760.github.io/pairmux/)
- [CLI reference and envelope schema](https://treeleaves30760.github.io/pairmux/cli-reference)
- [Human collaboration](https://treeleaves30760.github.io/pairmux/guides/human-collaboration)
- [Documentation source](./website/docs)
- [Release channels and remaining work](./RELEASING.md)

## License

pairmux is released under the [MIT License](./LICENSE).
