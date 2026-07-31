# Changelog

All notable changes to pairmux are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `pairmux prune [name] [--older-than 7d] [--dry-run]` reclaims retained journals: dead terminals'
  state directories and `.prev` rotation archives (including orphaned ones), never a live
  terminal's journal, and never a directory whose write lock is held. This is the documented exit
  for `kill`'s retain-the-journal default; `kill` and the large-journal guard now teach it.
- `doctor` reports the bytes retained under the state namespace with the largest terminal
  directories, and suggests `prune` once the total passes the large-journal threshold.
- The large-journal warning is now also appended to `wait` replies. Program terminals (dev servers,
  `tail -f`) are driven by `send`/`wait`/`peek` and previously never saw the guard even though they
  grow journals the fastest.

### Changed

- README, the docs site, and the Agent Skill now lead with what only pairmux provides — a real PTY
  for interactive programs, persistent shell state, live human handoff, and shared observation —
  and include an explicit "when to use it and when not to" table that sends short commands to the
  agent's own shell tool and plain long non-interactive commands to the harness's background mode.
  The install docs now cover the live signed APT repository (pairmux-apt) instead of calling it
  unavailable, and state the tmux ≥ 3.2 requirement up front.
- Secret-prompt recognition covers far more than English password prompts: PIN, OTP/MFA/2FA,
  verification/security/access codes, API/encryption/private keys, tokens and client secrets, plus
  the standard sudo password translations (zh-TW/zh-CN 密碼/密码, de Passwort, fr mot de passe,
  ja パスワード, es contraseña, pt senha, ko 암호, ru пароль) and fullwidth-colon endings.
  Multi-option confirmations (`[y/N/a]`, `(y/N/q)`) are now recognized too. A new
  `PAIRMUX_SECRET_PROMPT_RE` environment variable extends — never replaces — the builtin patterns;
  `doctor` validates it. Documentation now states plainly that recognition is best-effort and that
  a known credential prompt sitting quiet at `running` should be treated as a handoff candidate.

- A Homebrew cask (binary + man page, `depends_on formula: tmux`, quarantine-stripping postflight)
  is staged in the GoReleaser configuration with `skip_upload: true`. RELEASING.md documents the
  three owner-side activation steps (tap repository, `HOMEBREW_TAP_GITHUB_TOKEN` secret, flip to
  `auto`) and records the APT channel as live via the pairmux-apt repository.

### Fixed

- `run` now takes the command as exactly one argument, matching the MCP `pairmux_run` contract.
  The previous variadic form re-joined tokens on single spaces and typed the result into the shell,
  silently discarding quoting the caller's shell had already consumed (`pairmux run t git commit -m
  "two words"` committed with the message `two`). Multiple command tokens now return `E_BAD_ARGS`
  with a hint that reconstructs the correctly quoted single-argument command.

## [0.1.1] - 2026-07-20

### Changed

- PyPI wheels now use a dedicated Markdown project description with copyable installation and
  quickstart commands, precise platform requirements, Agent Skill and MCP entry points, and links to
  the documentation, changelog, and issue tracker. Core Metadata also advertises tmux as an external
  requirement and adds discovery keywords; the release workflow rejects wheels whose description
  fails PyPI's strict renderer check.
- The README and documentation now use reproducible JSON examples, distinguish direct `.deb`/`.rpm`
  downloads from the not-yet-implemented APT repository, and document prompt detection, future-only
  pattern waits, program terminals, notes, and human handoff without overstating their guarantees.
  The note command's self-teaching hint now distinguishes `run`/`peek` note fields from
  `wait --human`'s `output` result, and the nested-tmux attach hint no longer recommends an impossible
  cross-server `switch-client`.

## [0.1.0] - 2026-07-20

### Fixed

- Tag releases now build the complete GoReleaser archive/package/checksum set once, verify and preserve
  those exact bytes with the four wheels, then promote them without recompilation. GitHub remains a
  draft until PyPI succeeds, tag commits and workflow syntax are checked, and concurrent runs serialize.
- The wheel builder recognizes GoReleaser's dotted arm64 variant directories such as `v8.0`, validates
  both Mach-O architecture/deployment targets and ELF64 architecture, and rejects a binary whose
  native header does not match its wheel platform tag. Only canonical three-part SemVer tags are
  accepted; supported prerelease tags are normalized to PEP 440 wheel metadata.
- `install.sh` now stages the verified binary beside its destination and atomically renames it, replacing
  a pre-existing symlink instead of following it; it also requires the installed version to match the tag.
- Unsigned and unnotarized Homebrew casks are no longer advertised or published. That channel remains
  deferred until macOS signing and a real Gatekeeper installation test are available.
- Terminal names and tmux socket labels are validated before any filesystem or tmux operation. State
  now lives in an endpoint-specific hash of canonical `TMUX_TMPDIR`, uid, and socket label, preventing
  traversal and cross-server metadata collisions while retaining an in-place, non-migrating fallback
  for the historical conventional default-socket layout.
- Concurrent terminal creation uses a per-name reservation; journal preparation and command dispatch
  now close their failure paths so a partial tmux operation cannot leak a pane or leave a permanent
  `E_BUSY` command record.
- Fish 4+ uses its native OSC 133 integration. Fish installations that emit no ready mark degrade to
  the sentinel path using fish's `$status`, rather than the POSIX-only `$?` expansion.
- `wait --idle` now treats output silence as a prompt to refresh terminal state, not as proof that a
  command completed. Quiet running commands continue waiting; prompts and dead panes surface their
  actual status.
- Managed-pane discovery now uses a printable tmux format separator. Alpine's C locale can sanitize
  control-character tabs into underscores, which previously made a newly created live pane look dead.
- Bounded `peek` and default-`log` responses now report skipped raw prefixes as
  `truncated.omitted_bytes` and provide the executable `pairmux log NAME --range 1:end` recovery.
  Explicit `log --grep`, `--range`, and `--cmd` selections read the complete requested history;
  `--range A:end` selects through the final shaped line.
- Documentation builds override vulnerable transitive `serialize-javascript` and `uuid` releases
  with patched versions; `npm audit` now reports zero known vulnerabilities.

### Added

- `pairmux mcp serve` exposes the existing terminal commands as typed MCP `2025-11-25` stdio tools,
  preserving each `pairmux.v1` envelope as structured and text content without invoking a wrapper shell.
- `pairmux skill install` embeds the canonical Agent Skill and installs it atomically for Claude Code,
  Codex, Gemini CLI, Cursor, OpenCode, GitHub Copilot, Windsurf, Kiro, Amp, or the cross-agent
  `agents` location. Codex and `agents` share the standard `~/.agents/skills` destination, and
  `--target all` updates each existing agent directory only once.
- Terminal lifecycle and core loop: `pairmux new` (optional `--name`, `--cwd`, `--cmd`) opens a
  tmux-backed terminal; `run` blocks for command completion; `peek` returns a bounded recent view;
  `send` delivers interactive input; `log` returns a bounded recent view or complete explicitly
  selected history with `--cmd`, `--grep`, and `--range A:B`/`A:end`; `ls` lists status and activity;
  and `kill` ends terminals while retaining their journals.
- `wait` family: `pairmux wait <name>` blocks until the shell is truly idle (`--idle MS`, the default),
  a regex matches new output (`--pattern RE`), or a human note arrives (`--human`), with `--timeout`
  and a `--notify` desktop ping.
- Notes and human handoff: `pairmux note <name> <text>` records a message that surfaces in the
  driving agent's next `run`/`peek` envelope; `wait --human` instead returns `human-done` with the
  note text in `output`.
- Awaiting-input detection: a quiet command whose last screen line matches an interactive prompt
  (`[y/N]`, `password:`, pager `--More--`/`(END)`, "press any key") makes `run` return
  `awaiting-input` before its overall timeout. Password/passphrase/passcode prompts are classified as
  secrets and answered with a human-handoff hint rather than a guess.
- Human-facing commands: `pairmux attach [name]` takes over the live tmux session; `pairmux watch [--interval 2s]` renders a self-refreshing dashboard; `pairmux doctor` probes tmux version, per-shell completion tier, state-dir writability and the notification backend.
- Completion detection: injected OSC 133 integration for bash/zsh and native fish 4 marks are the
  primary signal; a shell-specific sentinel marker is the fallback. `doctor` distinguishes the
  diagnostic `hooks-no-C` tier from the stored terminal modes (`hooks` or `sentinel`).
- Journal and locking: each terminal streams raw output through `tmux pipe-pane` into a per-terminal
  journal (`raw.log` + `index.jsonl`, mode 0600); reads are lock-free, writes take a per-terminal
  writer `flock` and return an `E_BUSY` envelope naming the holder when contended. Attaching is a
  native tmux operation and records no event; humans leave an explicit `note` when handing back.
- Output shaping and `pairmux.v1` JSON envelopes: every non-interactive command replies through a
  self-describing envelope (`--json`) carrying `status`, ANSI-stripped and carriage-return-collapsed
  `output`, `truncated` recovery metadata when output is elided, `notes`, ordered `next` hints, and
  stable `error.code` values (`E_NO_TERMINAL`, `E_EXISTS`, `E_BUSY`, `E_DEAD`, `E_BAD_ARGS`,
  `E_TMUX`, `E_INTERNAL`).

[Unreleased]: https://github.com/treeleaves30760/pairmux/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/treeleaves30760/pairmux/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/treeleaves30760/pairmux/releases/tag/v0.1.0
