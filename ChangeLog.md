# Changelog

All notable changes to pairmux are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Terminal lifecycle and core loop: `pairmux new` (optional `--name`, `--cwd`, `--cmd`) opens a tmux-backed terminal; `pairmux run <name> <cmd> [--timeout 60s] [--head 50] [--tail 200]` sends a command and blocks until it completes, returning shaped `head`+`tail` output with `exit_code` and `duration_ms`; `pairmux peek <name> [--screen | --tail N]` returns recent output and status without blocking; `pairmux send <name> [--text S] [--key K] [--enter]` delivers raw input to a program; `pairmux log <name> [--cmd N | --grep RE | --range A:B]` reads full or filtered history; `pairmux ls` lists terminals with status, mode, current command, lock holder and last activity; `pairmux kill <name> | --all` ends terminals while retaining their journals.
- `wait` family: `pairmux wait <name>` blocks until a condition is met — output quiescence (`--idle MS`, the default), a regex match (`--pattern RE`), or a human note (`--human`) — with `--timeout` and a `--notify` desktop ping.
- Notes and human handoff: `pairmux note <name> <text>` records a message that surfaces in the driving agent's next `run`/`peek`/`wait` envelope; `wait --human` returns `human-done` when a human leaves a note.
- Awaiting-input detection: a quiet command whose last screen line matches an interactive prompt (`[y/N]`, `password:`, pager `--More--`/`(END)`, "press any key") is reported as `awaiting-input`; password/passphrase/passcode prompts are classified as secrets and answered with a human-handoff hint rather than a guess.
- Human-facing commands: `pairmux attach [name]` takes over the live tmux session; `pairmux watch [--interval 2s]` renders a self-refreshing dashboard; `pairmux doctor` probes tmux version, per-shell completion tier, state-dir writability and the notification backend.
- Three-tier completion detection: OSC 133 shell integration (bash/zsh hooks) is the primary signal, an injected sentinel marker is the fallback for other shells, and output quiescence is the backstop; `new` and `doctor` report the tier reached (`hooks`, `hooks-no-C`, `sentinel`).
- Journal and locking: each terminal streams raw output through `tmux pipe-pane` into a per-terminal journal (`raw.log` + `index.jsonl`, mode 0600); reads are lock-free, writes take a per-terminal writer `flock` and return an `E_BUSY` envelope naming the holder when contended.
- Output shaping and `pairmux.v1` JSON envelopes: every command replies through a self-describing envelope (`--json`) carrying `status`, ANSI-stripped and carriage-return-collapsed `output`, a `truncated.get_full` pointer when output is elided, `notes`, `next` step hints, and stable `error.code` values (`E_NO_TERMINAL`, `E_EXISTS`, `E_BUSY`, `E_DEAD`, `E_BAD_ARGS`, `E_TMUX`, `E_INTERNAL`).

[Unreleased]: https://github.com/treeleaves30760/pairmux/commits/main
