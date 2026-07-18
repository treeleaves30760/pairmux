# pairmux

Reliable terminal primitives for AI agents on top of tmux — with a human able to watch, attach, and help in the same live terminal.

Agents are bad at raw terminals for three separate reasons: they **guess `sleep N`** because nothing tells them when a command is done, `capture-pane` output is **dirty and truncated** (ANSI, spinners, scrolled-off history, no exit code), and existing headless tools **lock humans out** of the session entirely. pairmux closes all three gaps: a blocking CLI that returns exactly when the command finishes, a journal that keeps clean full history with exit codes, and native `tmux attach` so a human can step in and hand back.

## Install

pairmux is a single static Go binary. It needs **tmux >= 3.2** and runs on **macOS and Linux**.

```bash
# uv / pipx (PyPI platform wheels ship the binary)
uv tool install pairmux

# curl | bash (detects OS/arch, downloads the release, checks tmux)
curl -fsSL https://raw.githubusercontent.com/treeleaves30760/pairmux/main/install.sh | bash

# Homebrew
brew install treeleaves30760/tap/pairmux
```

Verify the environment at any time:

```bash
pairmux doctor
```

```text
pairmux doctor

✓  tmux        3.7b at /opt/homebrew/bin/tmux (>= 3.2)
✓  state dir   writable: ~/.local/state/pairmux
✓  live probe  zsh: hooks;  bash: hooks-no-C;  dash: sentinel
✓  notifier    osascript at /usr/bin/osascript (macOS notifications available)
```

## 60-second quickstart

Every command accepts `--json` for a machine-readable [`pairmux.v1` envelope](https://treeleaves30760.github.io/pairmux/cli-reference); without it you get a friendly text block.

```bash
# 1. Open a terminal (tmux window + journal + shell integration)
pairmux new --name build
```
```json
{"schema":"pairmux.v1","ok":true,"status":"created","terminal":"build","mode":"hooks","next":["pairmux run build \"echo hello\""]}
```

```bash
# 2. Run a command and block until it finishes — no sleeps, no guessing
pairmux run build "echo hello world"
```
```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"hello world"}
```

```bash
# 3. Long-running command: run returns "running" at the timeout, then keep waiting
pairmux run build "make -j4" --timeout 30s      # -> status: running, with a tail
pairmux wait build --idle 800                    # block until output goes quiet
pairmux peek build                               # look any time, read-only
```

```bash
# 4. Interactive program: pairmux surfaces the prompt as awaiting-input
pairmux run deploy "terraform apply"             # -> status: awaiting-input on [y/N]
pairmux send deploy --text yes --enter
```

```bash
# 5. Humans jump into the same live terminal
pairmux attach build      # take over the tmux session
pairmux watch             # live dashboard of every terminal
```

## Human handoff: a real transcript

The rule for the agent is simple: **never guess a secret.** When a command asks for a password, pairmux tells the agent to hand off, and a human answers in the same pane.

```bash
# Agent runs a migration that prompts for a DB password:
pairmux --json run dbmigrate "psql -h prod ..."
```
```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"dbmigrate","mode":"hooks",
 "output":"\nPassword: ",
 "next":["do NOT guess or type secrets","pairmux wait dbmigrate --human --notify   # hand off to the human"]}
```

```bash
# Agent hands off and blocks, pinging the human's desktop:
pairmux wait dbmigrate --human --notify
```

```bash
# Human takes over the exact same pane, types the password, and leaves a note:
pairmux attach dbmigrate                              # type the password in the pane
pairmux note dbmigrate "entered the db password"
```

```json
// The agent's wait unblocks the moment the note lands:
{"schema":"pairmux.v1","ok":true,"status":"human-done","terminal":"dbmigrate","mode":"hooks",
 "output":"entered the db password","next":["pairmux peek dbmigrate"]}
```

The agent resumes with `pairmux peek dbmigrate` and carries on. The password was never seen by, echoed to, or logged for the agent.

## Statuses and modes

A terminal's derived status (shown by `ls`, `peek`, `wait`):

| status           | meaning                                                          |
|------------------|-----------------------------------------------------------------|
| `idle`           | shell at a prompt, no command running                           |
| `running`        | a command is executing                                          |
| `awaiting-input` | running but quiet, last line looks like a prompt (answer it)    |
| `dead`           | the terminal's pane is gone; journal is retained                |

Commands also report an action status in their envelope: `created`, `done`, `sent`, `noted`, `killed`, `ok`, and `wait`'s outcomes `idle` / `pattern-found` / `human-done` / `timeout`. Errors carry `ok:false` and a stable `error.code`: `E_NO_TERMINAL`, `E_EXISTS`, `E_BUSY`, `E_DEAD`, `E_BAD_ARGS`, `E_TMUX`, `E_INTERNAL`.

Completion-detection **mode**, chosen per terminal at `new`:

| mode       | how completion is detected                                              |
|------------|------------------------------------------------------------------------|
| `hooks`    | OSC 133 shell integration (bash/zsh) — precise start/end + exit code   |
| `sentinel` | an injected `printf` marker carrying `$?` — fallback for other shells   |

## For AI agents

pairmux ships as an [Agent Skill](https://github.com/treeleaves30760/pairmux-skills). The skill teaches the golden loop (`run` -> `wait` -> read `status` -> `send`/`log`) and the ironclad rules (never `sleep` to guess timing; hand passwords to humans; prefer reading the log over re-running). It uses the open `SKILL.md` standard, so one canonical skill installs into Claude Code, Codex, Gemini CLI, Cursor, OpenCode, and other agents.

The envelopes are also self-teaching: every reply carries a `next` array of concrete commands, so an agent that skips the docs still gets pointed to the right next step.

## Docs and changelog

- Documentation: https://treeleaves30760.github.io/pairmux/
- CLI reference and the full `pairmux.v1` envelope schema: https://treeleaves30760.github.io/pairmux/cli-reference
- [ChangeLog.md](./ChangeLog.md)

## License

pairmux is released under the MIT License. See [LICENSE](./LICENSE).
