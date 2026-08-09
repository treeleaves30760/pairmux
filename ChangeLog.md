# Changelog

All notable changes to pairmux are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Prompt detection now reads the pane's terminal, not just the words on it. Echo suppressed while
  the kernel is still assembling a line is exactly what `getpass()` does — and what sudo, ssh, git,
  gpg, pinentry and npm therefore do — so a credential prompt is identified as one whatever
  language it is written in and whichever tool asked, including tools no pattern covers. Of
  eighteen real prompts, the pattern list recognised twelve; the ssh host-key question, terraform's
  `Enter a value:`, `[yes/No]`, `Username:` and a type-the-name confirmation were all invisible,
  which meant the terminal sat at `running` and a handoff over one could only end in a timeout.
  Every one of them is now detected, and every echo-suppressed prompt is refused rather than
  guessed at — the case where guessing wrong is worst is the one case that is now certain rather
  than heuristic.

  Wording remains a second source of evidence, and still promotes a prompt to secret for tools that
  ask in plain sight. A terminal that only goes quiet mid-line is reported as a third, weaker kind:
  it is how an unrecognised question surfaces at all, but it also describes a command that printed
  `Building... ` and went to work, so it needs ten seconds of silence and its reply says to look
  before answering and offers `--done` for carrying on. `run` is unaffected below that threshold,
  and never kills anything either way.

  The reading costs one ioctl on a device recorded at creation — no subprocess and no tmux
  round-trip — because several agents watching one terminal each pay for every poll.

### Fixed

- A program terminal could lose the first thing it printed. tmux starts a pane's command as soon as
  the window exists, which is before `pipe-pane` can be attached, so a program whose opening act is
  a prompt could print into a window nothing was recording and then sit there invisible for its
  whole life. Linux lost that race routinely while macOS won it. The pane now holds on a FIFO until
  the journal is capturing, turning the race into an ordering; the holding shell `exec`s the real
  command, so what runs in the pane, what `pane_current_command` reports, and how the terminal dies
  are all unchanged.

- The release job validated every credential except the one it used last. The
  Homebrew tap token was first exercised after PyPI — which cannot be
  republished for a tag — and after the GitHub release went public, so v0.3.0's
  `403 Resource not accessible by personal access token` could only surface in
  the one position where recovery has to be manual. The preflight now proves
  the tap token can reach the cask for stable tags, before anything publishes,
  and a manual **Tap credential** workflow proves read *and* write after a
  rotation: only Actions can read the secret, so a value mistyped into
  `gh secret set` is otherwise invisible until a release depends on it.

## [0.3.0] - 2026-08-09

### Added

- `pairmux wait <name> --done` blocks until the terminal's running command finishes — or until the
  next one does, if the terminal is idle right now — and reports its `exit_code`. `wait` takes no
  lock and records nothing, so any number of agents can hold a `--done` wait on one terminal and
  every one of them wakes on the same completion mark: a completion broadcast with nothing to
  register with and no daemon involved. It also covers a command a human typed into the pane
  directly. Shell terminals only; a `--cmd` program terminal emits no completion marks and gets
  `E_BAD_ARGS` with a workable alternative.
- `detect.CompletionWatcher`, a pollable form of the C-correlated completion detection `run`
  already used. `wait` has to watch a completion alongside notes, patterns and quiescence, so it
  drives the watcher from its own loop instead of blocking inside one.

### Fixed

- `wait --human` could only end in its own timeout. The handoff the docs teach — hit a secret
  prompt, `wait --human --notify`, let a human type the password in the pane — resolved *only* on a
  `pairmux note`, and a human who answers the prompt writes nothing to the journal: attaching
  records no event, and typing into a pane is not one either. The command would finish and the
  agent would keep blocking for the full 300s. `--human` now also resolves when the *human* is
  finished: the prompt is answered and the terminal is visibly moving again (`status: running`,
  whose `next` offers `--done` for following the command the rest of the way), or the command
  finishes outright (`status: done` with its `exit_code`). Reporting resumption rather than
  completion is the point — a password answered at second two can be followed by a five-minute
  migration, and the agent is released at second two. A rejected answer that re-prompts keeps the
  wait blocked, because the terminal is still awaiting input. A completion that landed before the
  wait started resolves it immediately, the same way an unseen note already does — an agent that
  hands off, does other work, and only then waits is not left blocking for a human who has gone.
- Adding `--idle` to `wait --human` was not a workaround: `awaiting-input` counts as a terminal
  state for an idle wait, so it returned instantly with the very prompt being handed off, and the
  agent's only move was to hand off again. A prompt no longer resolves a `--human` wait at all.
- A timed-out wait's retry hint dropped every condition flag: `wait <name> --human --notify` came
  back suggesting a bare `wait <name> --timeout 2x`, which at the handoff prompt is an idle wait
  that returns `awaiting-input` instantly — so an agent following its own `next` spun instead of
  waiting. The hint now reproduces the wait exactly (`--idle`/`--pattern`/`--done`/`--human`/
  `--notify`) with double the deadline, floored at 300s for a handoff so it cannot undercut the
  skill's own rule, and a `--human` timeout leads with "the human has not answered yet — do NOT
  type the secret".
- A handoff is released by the human's answer, not by their keystrokes. Typed characters echo into
  the journal one at a time, and an echoed answer stops the last line looking like a prompt well
  before the human commits to it — so "output appeared past the prompt" alone reported a human
  who was still deciding as a human who had finished. The line the prompt sat on must now be
  terminated (the newline an Enter produces, echoed by the tty or written by the program) before
  any verdict is reached, and the verdict is then held briefly so a rejected answer's re-prompt
  wins over the echo that preceded it.
- A resolved `--human` handoff returns no `output`. The span it would quote is the span a human was
  summoned to type into, so a program that echoes what it should not — or a secret prompt the
  heuristics never recognized — would have put the credential straight into the agent's context.
  The agent gets the fact and the exit code, and `peek`s if it wants the output.

## [0.2.0] - 2026-08-01

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
  is staged in the GoReleaser configuration, and the channel is now **active**: the release
  workflow preserves the rendered cask during validation and pushes `Casks/pairmux.rb` to the
  `treeleaves30760/homebrew-pairmux` tap after the release goes public (prereleases skipped,
  content read back and byte-compared). RELEASING.md documents the wiring, and the APT channel is
  recorded as live via the pairmux-apt repository.

### Fixed

- Documentation builds pick up the patched `fast-uri` release and override the vulnerable
  transitive `brace-expansion` versions with 5.0.9; `npm audit` reports zero known
  vulnerabilities again.
- The commit-subject validator pins `LC_ALL=C` so its `[a-z]` pattern brackets match bytes rather
  than locale collation. Under locales such as `zh_TW.UTF-8` the ranges also matched uppercase
  letters, silently accepting invalid subjects (and failing `--self-test`) on contributor machines
  while CI's C locale rejected them.
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

[Unreleased]: https://github.com/treeleaves30760/pairmux/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/treeleaves30760/pairmux/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/treeleaves30760/pairmux/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/treeleaves30760/pairmux/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/treeleaves30760/pairmux/releases/tag/v0.1.0
