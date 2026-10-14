# pairmux

**Let AI agents drive interactive terminal programs — and hand off to a human when they can't.**

[Home](https://pairmux.treeleaves30760.com) ·
[PyPI](https://pypi.org/project/pairmux/) ·
[Documentation](https://pairmux-docs.treeleaves30760.com/) ·
[CLI reference](https://pairmux-docs.treeleaves30760.com/cli-reference) ·
[Changelog](https://github.com/treeleaves30760/pairmux/blob/main/ChangeLog.md) ·
[Source](https://github.com/treeleaves30760/pairmux)

pairmux is a small Agent-Computer Interface layer over a terminal multiplexer. Agents get a real PTY
with persistent shell state, blocking command outcomes, retained history, and machine-readable
recovery hints. Humans keep normal access to the same live terminal: watch the agent work, take over
an interactive prompt, hand back.

pairmux drives terminals through **tmux** (3.2 or newer) when tmux is installed, and otherwise
through its own **built-in multiplexer** — real PTYs on macOS/Linux, ConPTY on Windows — so tmux is
optional and Windows runs natively. See [Terminal backends](#terminal-backends).

> **New in v1.0.0:** the built-in multiplexer, native Windows support and Windows artifacts.
> v0.6.0 and earlier require tmux ≥ 3.2 and run on Windows only inside WSL.

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

**tmux is optional.** On macOS and Linux, pairmux uses tmux when it is on PATH (the tmux backend
needs **tmux 3.2 or newer**: `brew install tmux` on macOS, `sudo apt install tmux` on Debian/Ubuntu,
or the Homebrew cask below) and its built-in multiplexer otherwise; Windows always uses the built-in
one. v0.6.0 and earlier still require tmux. PyPI wheels require **Python 3.9+** to install; uv can
obtain managed Python from Astral upstream if needed. The wheel bundles the prebuilt Go binary, so
the installed executable needs neither Python nor a Go toolchain to run.

| Platform | Architectures | Additional requirement |
| --- | --- | --- |
| macOS 12+ | x86-64, ARM64 | Python 3.9+ for wheel installation |
| Linux | x86-64, ARM64/aarch64 | PyPI wheels use `manylinux_2_17` / glibc 2.17+ tags; Python 3.9+ to install |
| Windows 10 1809+ / 11 | x86-64, ARM64 | Native from v1.0.0 (`win_amd64`/`win_arm64` wheels, release zips); earlier releases only inside WSL. See [Windows](#windows) |

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

**On Windows, `pairmux update` refuses with `E_UPDATE`.** uv installs Windows tools as copied `.exe`
files rather than the symlinked layout the updater verifies ownership through, and Windows cannot
replace an executable while it runs. Close running pairmux commands (such as `watch`, `attach`, or
an MCP client's `mcp serve`), then run `uv tool install --upgrade pairmux` (or `pip install -U
pairmux` in the environment that has it). Managed terminals may stay open: the built-in server runs
from its own copy of the executable (see [Terminal backends](#terminal-backends)).

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
bash "$installer_dir/install.sh" --version v1.0.0 --dry-run
```

Pin any published version, or omit `--version` for the latest stable PyPI release. Remove `--dry-run` only after a successful download and review
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

Available from v0.2.0; the cask is republished automatically with every stable release. Because the
cask installs tmux, tmux is the automatically selected backend on those machines.

### Other PyPI installers

Use `pipx`, or `pip` inside a dedicated Python 3.9+ virtual environment:

```bash
pipx install pairmux
# or, inside a dedicated environment:
python -m pip install pairmux
```

### Manual release archives

For installation without Python, download the matching macOS/Linux `.tar.gz` (or, from v1.0.0,
the Windows `pairmux_<version>_windows_amd64.zip` / `_windows_arm64.zip`) and
`checksums.txt` from [GitHub Releases](https://github.com/treeleaves30760/pairmux/releases). Verify the
archive's SHA-256 before extracting and installing the binary; the
[Getting Started guide](https://pairmux-docs.treeleaves30760.com/#install) has complete commands.
This manual path remains available, but the website's `install.sh` now uses PyPI.

### Windows

pairmux runs natively on Windows 10 1809 or newer (needed for ConPTY and AF_UNIX sockets), on
x86-64 and ARM64, using the [built-in multiplexer](#terminal-backends); neither tmux nor
WSL is needed. **This is new in v1.0.0**, which adds `win_amd64`/`win_arm64` wheels and Windows
release zips. It is validated by CI on hosted Windows Server 2025 runners and was accepted on real
Windows 11 x86-64 hardware (Windows PowerShell 5.1, cmd.exe and Git Bash terminals through ConPTY,
including a human `attach`); the ARM64 build has not been exercised at runtime.

Install it from PowerShell with any PyPI installer (`uv tool install pairmux`, `pipx install
pairmux`, or `python -m pip install pairmux` in a dedicated environment), or with the PowerShell
one-liner:

```powershell
irm https://pairmux.treeleaves30760.com/install.ps1 | iex
```

The one-liner applies the same policy as `install.sh`: uv installs the pairmux wheel exclusively from
`https://pypi.org/simple`, wheels only and without cache, ignoring inherited `UV_*` settings and uv
configuration; uv itself is bootstrapped from Astral's official installer if missing. It needs no
administrator rights and edits neither PATH nor profiles. A piped script takes no arguments, so set
`PAIRMUX_VERSION` or `PAIRMUX_INSTALL_DIR` in the environment first; `PAIRMUX_DRY_RUN` prints the
plan without installing. Pinning a version before v1.0.0 has no Windows wheel, so the native
install fails with a hint to use WSL. Without Python, use a release zip (`pairmux.exe` with LICENSE, README.md
and `man/pairmux.1`) and put `pairmux.exe` on PATH; see also
[Install from a clone](#install-from-a-clone).

**Shells.** A new terminal starts PowerShell 7 (`pwsh.exe`), else Windows PowerShell
(`powershell.exe`), else `%COMSPEC%` (normally `cmd.exe`). `$SHELL` is honored only if it names an
existing file — Git Bash exports `/usr/bin/bash`, which Windows cannot start. `PAIRMUX_SHELL`
overrides the shell on every platform:

```powershell
$env:PAIRMUX_SHELL = 'C:\Program Files\Git\bin\bash.exe'
```

PowerShell terminals use `hooks` mode: after your profile loads, an inline prompt wrapper emits
OSC 133 marks with the exit status (`$?`/`$LASTEXITCODE`). It is passed with `-NoExit
-EncodedCommand`, so the script execution policy does not apply to it. It emits no C mark, so
`doctor` reports it as `hooks-no-C`. `cmd.exe` uses `sentinel` mode (`cmd /v:on /k`, exit status
from `ERRORLEVEL`); Git Bash gets the same bash hooks shim as on Unix. `new --cmd` runs its command
through that shell: PowerShell `-EncodedCommand`, `cmd /s /c`, or `bash -lc`.

**What differs on Windows.** There is no tty line discipline to read, so secret-prompt
classification relies on prompt wording only (`PAIRMUX_SECRET_PROMPT_RE` extends it). `kill` ends
the terminal's whole process tree through a job object. `--notify` shows a best-effort notification
through Windows PowerShell. `pairmux update` refuses; see [Update](#update-v060).

**WSL remains an option.** Inside WSL, pairmux is an ordinary Linux program: it uses tmux if it is
installed in that distribution and the built-in multiplexer otherwise (v0.6.0 and earlier need tmux
there). To install into WSL with the one-liner — the default behavior before native support — opt in
through the environment:

```powershell
$env:PAIRMUX_INSTALL_TARGET = 'wsl'
$env:PAIRMUX_VERSION   = 'v1.0.0'   # optional: pin a published version
$env:PAIRMUX_WSL_DISTRO = 'Ubuntu'  # optional: a distribution other than the default
irm https://pairmux.treeleaves30760.com/install.ps1 | iex
```

The WSL target finds WSL, checks it has a distribution, and runs the Bash installer inside it;
`PAIRMUX_WSL_DISTRO` without the WSL target is refused rather than ignored. pairmux then lives inside
the distribution: run it as `wsl -- pairmux version`, or from a shell in
that distribution.

### Direct RPM packages

Stable GitHub releases include `.rpm` files for Linux x86-64 and ARM64. Download the matching
file and `checksums.txt` from [GitHub Releases](https://github.com/treeleaves30760/pairmux/releases),
verify its SHA-256 checksum, then install that local file (the
[Getting Started guide](https://pairmux-docs.treeleaves30760.com/#install) shows complete commands):

```bash
sudo dnf install ./downloaded-file.rpm
```

There is no Yum repository; download a new RPM when upgrading. Up to v0.6.0 the RPM requires
tmux ≥ 3.2; from v1.0.0 it only recommends tmux (a weak dependency), because pairmux runs without
it.

### Migrating from APT / Debian packages

pairmux's APT repository and historical `.deb` distribution were retired on 2026-10-08.
Debian/Ubuntu users should use PyPI/uv, Homebrew, or a manual archive; `apt install tmux` remains
the normal way to get tmux. Existing APT-installed pairmux commands may shadow a new user-level
installation.

The former setup created `/etc/apt/sources.list.d/pairmux.sources`,
`/etc/apt/preferences.d/pairmux.pref`, `/usr/share/keyrings/pairmux-archive-keyring.pgp`, and installed
the `pairmux-archive-keyring` package. Review and disable/remove only those pairmux-specific entries,
and review removal of the old `pairmux` and keyring packages without removing tmux. Follow the
[APT migration guide](https://pairmux-docs.treeleaves30760.com/migrating-from-apt)
for read-only inspection, safe cleanup ordering, and PATH checks. The documentation site is
available over HTTPS.
The installer does not perform system-package cleanup or overwrite commands owned by other installers.

### Install from a clone

If you can clone the repository but cannot reach PyPI, install from the checkout. The repository root
has a `pyproject.toml` whose in-tree build backend needs **no build dependencies**, so this works
offline with any Python 3.9+ (64-bit):

```bash
python -m pip install .           # into the active environment (uv pip install . also works)
uv tool install --offline .       # or as a persistent uv tool
uv tool install --offline --reinstall .   # rebuild later, after pulling changes
```

`pip install git+https://github.com/treeleaves30760/pairmux` does the same straight from GitHub.

The backend builds the binary with **Go 1.25+** if `go` is on PATH. Without Go, it packages a
prebuilt binary named by `PAIRMUX_PREBUILT_BINARY`, for example the `pairmux.exe` from a GitHub
Release zip. The binary must match the host OS and architecture: its Mach-O/ELF/PE header is checked
(it is never executed), and a wrong OS or architecture, a relative path, or a missing file is
refused. In PowerShell, from the clone, with the release zip downloaded (and its SHA-256 compared
with `checksums.txt`, e.g. via `Get-FileHash`) into the parent directory:

```powershell
Expand-Archive ..\pairmux_<ver>_windows_amd64.zip -DestinationPath ..\pairmux-download
$env:PAIRMUX_PREBUILT_BINARY = (Resolve-Path ..\pairmux-download\pairmux.exe).Path
python -m pip install .      # or: uv tool install --offline .
Remove-Item Env:PAIRMUX_PREBUILT_BINARY
```

Windows zips exist from v1.0.0; for older releases, a Windows install from a clone needs Go. On macOS/Linux, point the variable at
the `pairmux` binary from the matching `.tar.gz`. There is no sdist and no editable install
(`pip install -e .`); both refuse with an explanation, and `uv build` needs `--wheel`.

These are development builds, never release wheels. Their version comes from `git describe` against
the latest `vX.Y.Z` tag: `X.Y.Z.post<N>.dev0+g<12-character sha>`, with `.dirty` appended for
uncommitted changes. Even a tagged commit builds as `post0.dev0`, and without git metadata the
version is `0.0.0.dev0+unknown`. The same string is stamped into the binary, so a local build cannot
be mistaken for a PyPI release, and `pairmux update` refuses it as not a canonical release: update
such an install by rebuilding from the clone. `PAIRMUX_VERSION` overrides the version with a
canonical SemVer tag or PEP 440 version. The wheel is tagged for the host only (`macosx_12_0_<arch>`,
`linux_<arch>`, `win_amd64`, or `win_arm64`); the `linux_<arch>` tag installs on glibc and musl
alike, and PyPI would refuse it.

No Python at all? Put a release binary on PATH directly (see
[Manual release archives](#manual-release-archives)), or build it with Go as below.

### Build this checkout

Source builds require Go 1.25. On macOS/Linux:

```bash
make build
mkdir -p "$HOME/.local/bin"
install -m 0755 bin/pairmux "$HOME/.local/bin/pairmux"
pairmux version
```

On Windows (no `make` needed), build `pairmux.exe` and put its directory on PATH:

```powershell
go build -o pairmux.exe ./cmd/pairmux
.\pairmux.exe version
```

A plain `go build` does not stamp a release version; `make build` stamps `VERSION` (default
`0.1.0-dev`). After installation, check the selected backend, state access, shell integration, and
notification support. The live shell probe runs on an isolated throwaway endpoint of the selected
backend and does not disturb managed terminals:

```bash
pairmux doctor
```

## Terminal backends

pairmux needs something to own the live terminals between its short-lived commands. It has two
backends with the same commands, envelopes and journals:

- **`tmux`** (tmux ≥ 3.2), as in every earlier release: each terminal is a tmux pane.
- **`builtin`**: pairmux's own small server — real PTYs via `/dev/ptmx` on macOS/Linux, ConPTY on
  Windows. It is the same `pairmux` executable running a hidden subcommand, started on demand by the
  first command that needs it and detached from the caller's session or console. There is one
  server per endpoint (the `--socket` / `PAIRMUX_SOCKET` name), and it exits by itself after about
  30 seconds with no terminals and no connections, so there is no service to install or stop.

`PAIRMUX_BACKEND` selects one explicitly: `tmux`, `builtin`, or `auto` (the same as unset). The
automatic choice is made per endpoint, in this order:

1. Windows → `builtin` (`PAIRMUX_BACKEND=tmux` on Windows is an error).
2. A built-in server is already running for this socket → `builtin`, so terminals created before
   you installed tmux stay reachable afterwards — unless a tmux server is running for the same
   socket too, in which case `tmux` keeps its live terminals visible.
3. tmux 3.2 or newer is on PATH → `tmux`. Existing users see no change, and their live tmux
   terminals stay where they are. An older tmux is skipped in favor of `builtin`.
4. Otherwise → `builtin`.

Every terminal is created with `PAIRMUX_BACKEND` set to its own backend, so a nested pairmux layer
started inside it stays on the same backend. An invalid value is an `E_BAD_ARGS` error for terminal
commands; `help`, `version`, `doctor`, `skill`, and `update` still work so you can diagnose it.
`pairmux doctor` begins with a `backend` line naming the backend, why it was chosen, and the
built-in socket path or tmux version.

**Behavior is the same on both.** `new`, `run`, `peek`, `wait`, `send`, `log`, `ls`, `kill`, `note`,
`prune`, `watch`, and `mcp serve` behave identically. `peek --screen` on the built-in backend
renders a virtual screen with `tmux capture-pane -p` semantics. The built-in backend journals a
program's output from its very first byte, and both default to a 220x50 terminal. `E_TMUX` keeps
its name but now means "the terminal backend (tmux or the built-in server) failed"; built-in
failures carry built-in-specific hints. Only `attach` looks different (see
[human handoff](#interactive-work-and-human-handoff)).

**Separate state.** Built-in endpoints get their own hashed state namespace, separate from tmux
endpoints, so a terminal called `build` on each backend never collides. Each backend lists only its
own terminals, live or dead; select the other with `PAIRMUX_BACKEND` to see its terminals. The
legacy unnamespaced default-socket fallback remains tmux-only.

**Runtime directory.** The built-in server keeps its socket, single-instance lock, and diagnostic
log in a per-user runtime directory:

| Platform | Directory |
| --- | --- |
| macOS/Linux | `$PAIRMUX_TMPDIR`, else `$TMUX_TMPDIR`, else `/tmp`, then `pairmux-<uid>/` — created mode 0700; refused (by the server and by every client before it connects) if owned by another uid or accessible by group/others |
| Windows | `%PAIRMUX_TMPDIR%`, else `%LOCALAPPDATA%\pairmux`, then `run\` — created with an ACL admitting only you and SYSTEM; refused if another account owns it |

It holds `<socket>.sock`, `<socket>.lock`, and `<socket>.log` (server diagnostics; look there if the
server does not start); a server that exits cleanly removes all three. Socket paths are limited to about 100 bytes: an overly long socket name gets
a digest name, and if the directory itself is too deep the error tells you to set `PAIRMUX_TMPDIR`
to a short writable directory. On Windows the server runs from a private copy,
`pairmux-muxd-<hash>.exe`, in that directory, so an upgrade can replace the installed `pairmux.exe`
while terminals are open.

## Reproducible quickstart

The following sequence uses only standard macOS/Linux shell commands. Use a different valid name if
`demo` is already an active pairmux terminal. On Windows, the commands sent to the terminal are also
valid in its default PowerShell shell, where `echo` and `sleep` are built-in aliases.

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
replace) the builtin secret recognition for your environment; `pairmux doctor` validates it. On
macOS/Linux, a prompt that turns terminal echo off is classified as secret whatever its wording;
Windows has no tty line discipline to read, so there the wording is the only evidence.

This safe demo creates its terminal first and uses a disposable input value. The simulated program
turns terminal echo off before reading (POSIX shell; a PowerShell variant follows):

```bash
# Terminal A: create a terminal and start the prompt.
pairmux --json new --name handoff
pairmux --json run handoff "sh -c 'printf \"Password: \"; stty -echo; IFS= read -r secret; stty echo; printf \"\\ninput received\\n\"'"

# Once run reports awaiting-input, hand off and block.
pairmux --json wait handoff --human --notify --timeout 5m
```

`sh` and `stty` do not exist in a Windows PowerShell terminal. There, start the prompt with
`Read-Host`, which masks what is typed; the single quotes keep the calling shell from expanding `$p`:

```powershell
pairmux --json run handoff '$p = Read-Host -AsSecureString -Prompt Password; echo done'
```

From a second interactive terminal, attach to the same pane. On the tmux backend, run this outside an
existing tmux client:

```bash
pairmux attach handoff
```

Type a disposable value such as `demo-only` and press Enter, then detach: on the tmux backend with
tmux's configured binding (default `Ctrl-b d`), on the built-in backend always with `Ctrl-b d`, after
which it prints `[detached from handoff] tell the agent what you did: pairmux note handoff "..."`.
Back in the second shell, leave the hand-back note:

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

`attach` needs an interactive terminal (stdin and stdout). Its two backends behave differently:

- **tmux backend:** `attach` replaces itself with a native tmux client on pairmux's session, focused
  on the named window. It deliberately refuses to nest inside tmux. If you are already in tmux,
  detach to the outer shell first or use another terminal, then run `pairmux attach`. A tmux
  `switch-client` cannot cross from another server to pairmux's named socket.
- **built-in backend:** `attach` connects *this* terminal straight to the pane — no session or
  window list, no status bar — using the alternate screen, so your own scrollback is preserved.
  `Ctrl-b d` detaches, `Ctrl-b Ctrl-b` sends a literal `Ctrl-b`, and any other key after the prefix
  is dropped. With no name it opens the only live terminal; with several it fails with
  `E_BAD_ARGS` listing their names. It works inside tmux, but refuses to attach a pane to itself.
  While attached, the pane takes your terminal's size, and it returns to 220x50 when the last human
  detaches. Several humans may attach at once; the most recent attach or resize sets the size. If
  the program exits while you are attached, `attach` prints the exit message and returns.

On either backend, `send` warns the agent when a human is attached. `note` does not detach a client
or enforce exclusive control; it records a coordination event that releases `wait --human` and
tells the agent *what* you did, which a bare answer in the pane cannot.

`--notify` is best-effort and needs `osascript` on macOS, `notify-send` on Linux, or Windows
PowerShell (`powershell.exe`) on Windows. Human
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
| `dead` | The pane is gone; its journal remains available |

`run` reports `done`, `running`, or `awaiting-input`; `wait` can also report `idle`,
`pattern-found`, `human-done`, `dead`, or `timeout`. See the
[CLI reference](https://pairmux-docs.treeleaves30760.com/cli-reference) for every command status,
field, flag, and exit behavior.

Errors set `ok:false` and include a stable `error.code`: `E_NO_TERMINAL`, `E_EXISTS`,
`E_BUSY`, `E_DEAD`, `E_BAD_ARGS`, `E_TMUX`, `E_UPDATE` (v0.6.0+), or `E_INTERNAL`. `E_TMUX` keeps its
historical name but means the terminal backend — tmux or the built-in server — failed. Actionable
replies carry an ordered `next` array; safety or information entries may precede the first
executable command.

## Completion modes

`new` records either `hooks` (OSC 133 marks injected for bash/zsh and PowerShell, or emitted by
fish 4+) or `sentinel` (a shell-specific marker carrying the previous exit status, as for `cmd.exe`).
If a nominal hooks shell emits no ready mark, `new` records `sentinel`. `doctor` may call bash 3.2
and PowerShell `hooks-no-C` (they emit no C mark); their stored terminal mode is still `hooks`.

Program terminals created with `new --cmd` also report `sentinel` to keep the envelope's mode field
two-valued, but they do not accept `run` or receive per-command sentinel markers. Drive them with
`send`/`wait`/`peek`; their status follows the program pane's liveness and recognized prompts.

## State and retained history

The state root is `$PAIRMUX_STATE_DIR`, `$XDG_STATE_HOME/pairmux`, or
`~/.local/state/pairmux`. Each endpoint gets an isolated `<root>/.sockets/<sha256>/` namespace, and
built-in endpoints hash differently from tmux endpoints, so equal terminal names on different
endpoints or backends do not share metadata. Historical default-endpoint tmux state remains readable
and is never moved implicitly.

`peek` and unqualified `log` are bounded. A truncated reply provides `truncated.get_full`, such as
`pairmux log demo --range 1:end`. Explicit `log --cmd`, `--grep`, and `--range` selectors scan the
complete selected history and may return large results.

`peek`, `log`, `ls`, and `wait` record no journal events. Neither kind of `attach` (a native tmux
client, or the built-in direct attach) is an event; a human records the hand-back explicitly with
`pairmux note`.

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
