# pairmux

Reliable terminal primitives for AI agents on top of tmux — with a human able to watch, attach, and help in the same live terminal.

Agents are bad at raw terminals for three separate reasons: they **guess `sleep N`** because nothing tells them when a command is done, `capture-pane` output is **dirty and truncated** (ANSI, spinners, scrolled-off history, no exit code), and existing headless tools **lock humans out** of the session entirely. pairmux closes all three gaps: blocking calls that return on completion, input, requested conditions, or timeout; a journal that keeps clean full history with exit codes; and native `tmux attach` so a human can step in and hand back.

## Install

pairmux is a single static Go binary. It needs **tmux >= 3.2** and runs on **macOS and Linux**.

The project is currently pre-release: the source repository is public, but no release has been
published to GitHub or PyPI yet. From this checkout, build and install the binary locally:

```bash
make build
install -m 0755 bin/pairmux ~/.local/bin/pairmux
pairmux version
```

The following channels are implemented and release-tested, but become usable only after the first
public tagged release:

```bash
uv tool install pairmux
curl -fsSL https://raw.githubusercontent.com/treeleaves30760/pairmux/main/install.sh | bash
```

Homebrew distribution is deferred until the macOS binaries can be signed, notarized, and exercised
through Gatekeeper in CI. The direct archive and wheel channels do not claim cask support.

Verify the environment at any time:

```bash
pairmux doctor
```

```text
pairmux doctor

✓  tmux        3.7b at /opt/homebrew/bin/tmux (>= 3.2)
✓  state dir   writable: ~/.local/state/pairmux
✓  live probe  zsh: hooks;  bash: hooks-no-C;  fish: hooks;  dash: sentinel
✓  notifier    osascript at /usr/bin/osascript (macOS notifications available)
```

## 60-second quickstart

Every non-interactive command accepts `--json` for a machine-readable `pairmux.v1` envelope;
without it you get a friendly text block. `attach` and `watch` are native interactive human interfaces.

```bash
# 1. Open a terminal (tmux window + journal + shell integration)
pairmux new --name build
```
```json
{"schema":"pairmux.v1","ok":true,"status":"created","terminal":"build","mode":"hooks","next":["pairmux run build \"echo hello\""]}
```

```bash
# 2. Run a command and block until it finishes, needs input, or times out
pairmux run build "echo hello world"
```
```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"hello world"}
```

```bash
# 3. Long-running command: run returns "running" at the timeout, then keep waiting
pairmux run build "make -j4" --timeout 30s      # -> status: running, with a tail
pairmux wait build --idle 800                    # block until the shell is truly idle
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

Commands also report an action status in their envelope: `created`; `run`'s `done` / `running` / `awaiting-input`; `sent`, `noted`, `killed`, `ok`; and `wait`'s `idle` / `awaiting-input` / `pattern-found` / `human-done` / `dead` / `timeout`. Errors carry `ok:false` and a stable `error.code`: `E_NO_TERMINAL`, `E_EXISTS`, `E_BUSY`, `E_DEAD`, `E_BAD_ARGS`, `E_TMUX`, `E_INTERNAL`.

Completion-detection **mode**, chosen per terminal at `new`:

| mode | how completion is detected |
|------|----------------------------|
| `hooks` | OSC 133 marks: injected for bash/zsh and emitted natively by fish 4+ |
| `sentinel` | an injected `printf` marker carrying `$?` (or fish's `$status`) |

If a nominal hooks shell emits no ready mark, `new` records `sentinel` instead. This is how fish
versions/configurations without native OSC 133 support degrade safely. `doctor` may report the more
specific diagnostic tier `hooks-no-C` for bash 3.2; the terminal's stored mode remains `hooks`.

## State and history

The state root is `$PAIRMUX_STATE_DIR`, `$XDG_STATE_HOME/pairmux`, or
`~/.local/state/pairmux`. Each tmux endpoint gets an isolated
`<root>/.sockets/<sha256>/` namespace; the hash covers the canonical `TMUX_TMPDIR`, uid, and socket
label. Equal terminal names on separate tmux servers therefore cannot share metadata. Journals from
the historical `<root>/<terminal>/` layout remain readable only for the conventional default endpoint
and are never moved implicitly.

`peek` and an unqualified `log` deliberately return bounded recent views. If a byte prefix was skipped,
`truncated.omitted_bytes` says how much and `get_full` is an executable command such as
`pairmux log build --range 1:end`. Explicit `log --cmd N`, `--grep RE`, and
`--range A:B`/`A:end` read the complete selected history and can therefore return large results.
Read-only `peek`, `log`, `ls`, and `wait` calls record no events. `attach` is also just a native tmux
operation; a human records the hand-back explicitly with `pairmux note`.

## For AI agents

pairmux embeds its canonical Agent Skill. The skill teaches the golden loop (`run` -> `wait` -> read
`status` -> `send`/`log`) and the ironclad rules (never `sleep` to guess timing; hand passwords to
humans; prefer reading the log over re-running). Install one target explicitly, or update only the
agent configuration directories that already exist:

```bash
pairmux skill install --target codex
pairmux skill install --target all
```

Supported targets are Claude Code, Codex, Gemini CLI, Cursor, OpenCode, GitHub Copilot, Windsurf,
Kiro, Amp, and the shared `~/.agents/skills` location (`codex` and `agents` are aliases). The companion `pairmux-skills` repository
is the public source of truth; release changes are synced into the embedded copy in this repository.

The envelopes are also self-teaching: actionable replies carry an ordered `next` array. An agent obeys
any leading safety/information entries, then runs the first executable command.

## MCP clients

`pairmux mcp serve` exposes the same terminal operations as typed tools over the
[MCP `2025-11-25` stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports).
Point an MCP client at the binary directly (the exact configuration key varies by client):

```json
{
  "command": "pairmux",
  "args": ["mcp", "serve"]
}
```

The server advertises `pairmux_new`, `pairmux_run`, `pairmux_peek`, `pairmux_wait`,
`pairmux_send`, `pairmux_log`, `pairmux_ls`, `pairmux_kill`, `pairmux_note`, and
`pairmux_doctor`. Tool calls execute the existing CLI with argv arrays, never through a wrapper shell.
Each result includes the original `pairmux.v1` envelope as both `structuredContent` and JSON text for
older clients; pairmux errors are tool results with `isError: true` so a model can follow their recovery
hints. Tool subprocess stdout is capped at 1 MiB to protect the server; a larger result becomes an
actionable tool error, so narrow `pairmux_log` with `command_id`, `grep`, or a smaller `range`. Clients
should require confirmation for commands, input, and terminal kills that can have side effects.

## Docs and changelog

- Documentation source: [`website/docs`](./website/docs)
- CLI reference and the full `pairmux.v1` envelope schema: [`website/docs/cli-reference.md`](./website/docs/cli-reference.md)
- [ChangeLog.md](./ChangeLog.md)
- Release channels and remaining work: [RELEASING.md](./RELEASING.md)

A hosted GitHub Pages site becomes available after Pages is enabled with GitHub Actions as its source.

## License

pairmux is released under the MIT License. See [LICENSE](./LICENSE).
