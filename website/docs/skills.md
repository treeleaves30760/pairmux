---
title: Agent Skills
description: Install the canonical pairmux skill and benchmark it across OpenCode, Claude Code, and Codex.
---

# Agent Skills

pairmux embeds one canonical `SKILL.md` package. It teaches agents when to use pairmux, the
`new -> run -> wait/send/log` loop, secret handoff, and how to interpret `pairmux.v1` envelopes.

## Install

```bash
pairmux skill install --target agents --dry-run
pairmux skill install --target opencode
pairmux skill install --target all
```

`all` is conservative: it writes only where an agent's configuration directory already exists.

| target | global skill directory |
|---|---|
| `claude-code` | `~/.claude/skills/pairmux/` |
| `codex` | `~/.codex/skills/pairmux/` |
| `gemini` | `~/.gemini/skills/pairmux/` |
| `cursor` | project-relative `.cursor/skills/pairmux/` |
| `opencode` | `~/.config/opencode/skills/pairmux/` |
| `copilot` | `~/.copilot/skills/pairmux/` |
| `windsurf` | `~/.codeium/windsurf/skills/pairmux/` |
| `kiro` | `~/.kiro/skills/pairmux/` |
| `amp` | `~/.config/amp/skills/pairmux/` |
| `agents` | `~/.agents/skills/pairmux/` (cross-agent alias) |

The companion `pairmux-skills` checkout is the source of truth and includes the full install map. It
becomes a public repository alongside pairmux at the first release; until then, the binary's embedded
copy is the supported installer source.

## Cross-agent evals

The S01-S10 suite covers finite and slow commands, large logs, confirmations, secret handoff, REPLs,
pagers, a dev server, Ctrl-C recovery, and human notes. Its runner isolates every episode in a copied
worktree with a unique tmux endpoint and records exact pairmux argv through a PATH proxy.

```bash
python3 evals/run.py --agent opencode --model opencode/big-pickle --scenario S01-S10 --repeat 3
python3 evals/run.py --agent claude --scenario S01-S09
python3 evals/run.py --agent codex --scenario S01-S06,S08
```

Each run writes JSONL episode results plus JSON and Markdown summaries with pass rate, step count,
wall time, failure class, tool/model versions, and artifact paths. OpenCode Big Pickle is the minimum
cross-agent canary; repeated episodes matter because that model alias can change over time.
