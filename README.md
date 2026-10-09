# pairmux

**Let AI agents drive interactive terminal programs — and hand off to a human when they can't.**

[Home](https://pairmux.treeleaves30760.com) ·
[PyPI](https://pypi.org/project/pairmux/) ·
[Documentation](https://pairmux-docs.treeleaves30760.com/) ·
[CLI reference](https://pairmux-docs.treeleaves30760.com/cli-reference) ·
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

### Persistent install with uv

With [uv](https://docs.astral.sh/uv/getting-started/installation/) installed:

```bash
uv tool install pairmux
pairmux version
pairmux doctor
```

Keep uv's tool executable directory on PATH and check `command -v pairmux` for an older command
that may shadow it. This ordinary uv install retains your environment and uv configuration,
including index overrides, and may reuse cached downloads.

pairmux requires **tmux 3.2 or newer** at runtime. Install it separately (`brew install tmux` on
macOS, `sudo apt install tmux` on Debian/Ubuntu), or use the Homebrew cask below. PyPI wheels require
**Python 3.9+** to install; uv can obtain managed Python from Astral upstream if needed. The wheel
bundles the prebuilt Go binary, so the installed executable needs neither Python nor a Go toolchain
to run.

| Platform | Architectures | Additional requirement |
| --- | --- | --- |
| macOS 12+ | x86-64, ARM64 | Python 3.9+ for wheel installation |
| Linux | x86-64, ARM64/aarch64 | PyPI wheels use `manylinux_2_17` / glibc 2.17+ tags; Python 3.9+ to install |
| Windows | No native artifact | Runs inside WSL; see [Windows](#windows) |

### Update (v0.6.0+)

Starting with **v0.6.0**, update the currently running, verified persistent uv tool installation
(including one created by the Bash/WSL installer):

```bash
pairmux update
```

**v0.5.3 and older have no `update` command.** First run `uv tool install --upgrade pairmux` with
your normal uv configuration to obtain v0.6.0+ when available; then use `pairmux update`.

The updater requires uv to be installed. It clears all inherited `UV_*` settings, ignores uv
configuration, and reinstalls the latest compatible **stable wheel** from **`https://pypi.org/simple`**
with no persistent cache or source builds, replacing old exact pins, constraints, indexes, and
extras. It never selects below the running version's numeric core; recognized alpha/beta/rc builds
may advance to that core's final or a later stable release, while dev/snapshot builds are refused.

A version change returns `updated`; a same-version reinstall returns `refreshed`, not a no-op.
Failures return `E_UPDATE` with a recovery hint, without guaranteed rollback. Recover with the
original manager, or `uv tool install --upgrade pairmux` for uv installs (normal configuration
applies). Homebrew, RPM, pipx/pip, manual/development installs, and temporary uvx runs are unchanged;
tmux is untouched. There are no command-specific options, automatic updates, sudo, or uv bootstrap/update.
Global `--json` is supported; see the
[CLI reference](https://pairmux-docs.treeleaves30760.com/cli-reference#update-v060) for the contract.

### Alternative: PyPI installer

```bash
curl -fsSL https://pairmux.treeleaves30760.com/install.sh | bash
```

This Bash installer persistently installs the `pairmux` wheel with uv from **PyPI**, not a GitHub
archive. It bootstraps uv from [Astral's official installer](https://astral.sh/uv/install.sh) if absent
and lets uv manage a compatible Python when needed. Those uv/Python bootstrap downloads are separate
from the pairmux wheel's PyPI source. It installs to `~/.local/bin` by default, without sudo or
shell-profile edits, and checks for tmux without installing it.

The installer ignores inherited `UV_*` settings and uv configuration/index overrides, uses only
`https://pypi.org/simple` for pairmux, and requires a wheel rather than a source build. It reinstalls
with uv's persistent cache disabled so an existing tool or shared cache cannot stand in for the
PyPI wheel. Ordinary `uvx` / `uv tool install` commands use your normal configuration and may reuse
cache; that is not a fresh-download guarantee.

Prefer to inspect the script first? Download it successfully before reviewing and executing it:

```bash
installer_dir=$(mktemp -d) &&
curl -fsSL https://pairmux.treeleaves30760.com/install.sh -o "$installer_dir/install.sh" &&
less "$installer_dir/install.sh" &&
bash "$installer_dir/install.sh" --version v0.6.0 --dry-run
```

Use the `v0.6.0` pin only after publication; until then choose a published version or omit `--version`
for the latest stable PyPI release. Remove `--dry-run` only after a successful download and review
to perform the installation.
The downloaded installer's `--dry-run` prints its plan without network access or writes; it does not
install anything. `PAIRMUX_INSTALL_DIR` selects the uv tool executable directory. The
pipeline is a convenience, not a complete-download-before-execution check. It cannot update the
parent shell's PATH; follow the printed PATH hint and check `command -v pairmux` for older commands
that may shadow the new installation.

### Optional temporary run with uvx

Try pairmux without adding a persistent command to PATH:

```bash
uvx pairmux version
uvx pairmux doctor
```

`uvx` remains supported. It uses a temporary tool environment, which uv may cache, or reuses an
existing uv-managed installation. It does not promise a fresh download or the latest version on
every invocation; temporary runs cannot use `pairmux update`.

### Homebrew (macOS and Linuxbrew)

The tap cask installs pairmux **and its tmux dependency** in one command, and strips the
quarantine attribute so the binary runs without a Gatekeeper prompt:

```bash
brew install --cask treeleaves30760/pairmux/pairmux
```

Available from v0.2.0; the cask is republished automatically with every stable release.

### Other PyPI installers

Use `pipx`, or `pip` inside a dedicated Python 3.9+ virtual environment:

```bash
pipx install pairmux
# or, inside a dedicated environment:
python -m pip install pairmux
```

### Manual release archives

For installation without Python, download the matching macOS/Linux `.tar.gz` and `checksums.txt`
from [GitHub Releases](https://github.com/treeleaves30760/pairmux/releases). Verify the archive's
SHA-256 before extracting and installing the binary; the
[Getting Started guide](https://pairmux-docs.treeleaves30760.com/#install) has complete commands.
This manual path remains available, but the website's `install.sh` now uses PyPI.

### Windows

tmux does not run on Windows, so neither does pairmux — there is no native Windows artifact. The
supported arrangement is pairmux inside WSL, driving terminals there, which is what the PowerShell
one-liner sets up: it finds WSL, checks it has a distribution, and runs the Bash installer inside
it. Configure it through the environment, since a piped script takes no arguments:

```powershell
$env:PAIRMUX_VERSION   = 'v0.6.0'   # optional: after v0.6.0 is published
$env:PAIRMUX_WSL_DISTRO = 'Ubuntu'  # optional: a distribution other than the default
irm https://pairmux.treeleaves30760.com/install.ps1 | iex
```

pairmux then lives inside the distribution: run it as `wsl -- pairmux version`, or from a shell in
that distribution. Install tmux there too (`sudo apt install tmux`) if it is not already present.

### Direct RPM packages

Stable GitHub releases include `.rpm` files for Linux x86-64 and ARM64. Download the matching
file and `checksums.txt` from [GitHub Releases](https://github.com/treeleaves30760/pairmux/releases),
verify its SHA-256 checksum, then install that local file (the
[Getting Started guide](https://pairmux-docs.treeleaves30760.com/#install) shows complete commands):

```bash
sudo dnf install ./downloaded-file.rpm
```

There is no Yum repository; download a new RPM when upgrading.

### Migrating from APT / Debian packages

pairmux's APT repository and historical `.deb` distribution were retired on 2026-10-08.
Debian/Ubuntu users should use PyPI/uv, Homebrew, or a manual archive; `apt install tmux` remains
the normal dependency installation. Existing APT-installed pairmux commands may shadow a new
user-level installation.

The former setup created `/etc/apt/sources.list.d/pairmux.sources`,
`/etc/apt/preferences.d/pairmux.pref`, `/usr/share/keyrings/pairmux-archive-keyring.pgp`, and installed
the `pairmux-archive-keyring` package. Review and disable/remove only those pairmux-specific entries,
and review removal of the old `pairmux` and keyring packages without removing tmux. Follow the
[APT migration guide](https://pairmux-docs.treeleaves30760.com/migrating-from-apt)
for read-only inspection, safe cleanup ordering, and PATH checks. The documentation site is
available over HTTPS.
The installer does not perform system-package cleanup or overwrite commands owned by other installers.

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
[CLI reference](https://pairmux-docs.treeleaves30760.com/cli-reference) for every command status,
field, flag, and exit behavior.

Errors set `ok:false` and include a stable `error.code`: `E_NO_TERMINAL`, `E_EXISTS`,
`E_BUSY`, `E_DEAD`, `E_BAD_ARGS`, `E_TMUX`, `E_UPDATE` (v0.6.0+), or `E_INTERNAL`. Actionable replies
carry an ordered `next` array; safety or information entries may precede the first executable command.

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
[Agent Skills guide](https://pairmux-docs.treeleaves30760.com/skills). The companion
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

- [Getting started and task guides](https://pairmux-docs.treeleaves30760.com/)
- [CLI reference and envelope schema](https://pairmux-docs.treeleaves30760.com/cli-reference)
- [Human collaboration](https://pairmux-docs.treeleaves30760.com/guides/human-collaboration)
- [Documentation source](./website/docs)
- [Release channels and remaining work](./RELEASING.md)

## License

pairmux is released under the [MIT License](./LICENSE).
