---
title: Agent Skills
description: Install the canonical pairmux skill and benchmark it across OpenCode, Claude Code, and Codex.
---

# Agent Skills

pairmux embeds one canonical `SKILL.md` package. It teaches agents when to use pairmux, the
`new -> run -> wait/send/log` loop, secret handoff, and how to interpret `pairmux.v1` envelopes.
Agents submit work first, estimate when useful output should appear, and inspect startup after a
short bounded wait (usually about 30 seconds) before unrelated work or a longer wait. Human
credential handoffs retain their separate, at-least-300-second wait policy.
It recommends a persistent `uv tool install pairmux` setup and, from v0.6.0, `pairmux update`
only with explicit user authorization; agents must not install or self-update during terminal
workflows on their own. See [Getting Started](./getting-started.mdx#install) for CLI installation.

## Install

```bash
pairmux skill install --target agents --dry-run
pairmux skill install --target opencode
pairmux skill install -t all
```

`-t` is a short alias for `--target`. For example, `pairmux skill install -t cline` installs
Cline's skill even if its configuration directory does not exist yet.

`all` is conservative: it writes only where an agent's configuration directory already exists.
Cursor is relative to the current working directory, not a global installation.

Installation is offline: it copies the **running executable's embedded bundle**, not the newest
checkout or a remote skill. Both installation and `--dry-run` identify the CLI version and bundle
SHA-256. After an authorized CLI update, invoke `pairmux skill install --target all` again using
the updated executable, then restart or reload the coding agent. A source checkout must be rebuilt
before its skill edits can be installed; running an old binary can reinstall an old bundle.

Each installation replaces the bundle's filenames, including any local edits to those files.
Other files are preserved; legacy directories such as `~/.codex/skills/pairmux/` are not migrated
or deleted. Back up customized bundled files before refreshing.

The digest covers the sorted, slash-separated relative paths and contents: SHA-256 of each
`path + NUL + SHA-256(file bytes)` entry concatenated in order (the inner hashes are raw bytes).
This identifies skill-only changes even when the CLI version has not changed.

| target | installation directory |
|---|---|
| `claude-code` | `~/.claude/skills/pairmux/` |
| `codex` | `~/.agents/skills/pairmux/` |
| `gemini` | `~/.gemini/skills/pairmux/` |
| `cursor` | project-relative `.cursor/skills/pairmux/` |
| `opencode` | `~/.config/opencode/skills/pairmux/` |
| `copilot` | `~/.copilot/skills/pairmux/` |
| `windsurf` | `~/.codeium/windsurf/skills/pairmux/` |
| `kiro` | `~/.kiro/skills/pairmux/` |
| `amp` | `~/.config/amp/skills/pairmux/` |
| `cline` | `~/.cline/skills/pairmux/` |
| `roo-code` | `~/.roo/skills/pairmux/` |
| `kilo-code` | `~/.kilo/skills/pairmux/` |
| `droid` (Factory) | `~/.factory/skills/pairmux/` |
| `agents` | `~/.agents/skills/pairmux/` (compatibility alias for the shared standard) |

`codex` and `agents` intentionally resolve to the same open Agent Skills directory; `all` writes that
destination once. The companion [`pairmux-skills`](https://github.com/treeleaves30760/pairmux-skills)
repository is the source of truth and includes the full install map. The binary contains a
release-synchronized embedded copy.

## Cross-agent evals

The S01-S10 suite covers finite and slow commands, large logs, confirmations, secret handoff, REPLs,
pagers, a dev server, Ctrl-C recovery, and human notes. Its runner isolates every episode in a copied
worktree with a unique tmux endpoint and records exact pairmux argv through a PATH proxy. The eval
runner is not part of this pairmux checkout: clone the companion repository and run these commands
from its root (see its [eval README](https://github.com/treeleaves30760/pairmux-skills/blob/main/evals/README.md)):

```bash
git clone https://github.com/treeleaves30760/pairmux-skills.git
cd pairmux-skills
python3 evals/run.py --agent opencode --model opencode/big-pickle --scenario S01-S10 --repeat 3
python3 evals/run.py --agent claude --scenario S01-S09
python3 evals/run.py --agent codex --scenario S01-S06,S08
```

Each run writes JSONL episode results plus JSON and Markdown summaries with pass rate, step count,
wall time, failure class, tool/model versions, and artifact paths. OpenCode Big Pickle is the minimum
cross-agent canary; repeated episodes matter because that model alias can change over time.
