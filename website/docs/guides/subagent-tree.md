---
title: Sub-agent trees (opt-in)
description: "On request, let a coding agent run its sub-agents as pairmux terminals instead of its native sub-agents: a tree of any depth, every node a real CLI you can attach to."
---

# Sub-agent trees (opt-in)

Coding agents such as Claude Code have their own sub-agent mechanism (the Task/Agent tool). It is
usually one level deep: a sub-agent cannot start sub-agents of its own. With pairmux, each sub-agent
is a full agent CLI (`claude`, `codex`, …) in its own terminal, so any node can open the next layer,
and the tree can be as deep as the work needs.

**This is not the default.** The pairmux skill tells agents to keep using their host's native
sub-agents. An agent switches to a pairmux tree only when you ask for it explicitly, for example:

> use pairmux to build a hierarchy of sub-agents

> 使用 pairmux 來構建階層式的管理 Subagents

## What you get

- **Depth.** A child can start its own children the same way; `pairmux ls --tree` shows them all
  (`api`, `api/tests`, `web`, …).
- **Real terminals you can enter.** Attach to any node with `pairmux attach` or the
  [TUI](./tui.md), watch it, take over, and hand back.
- **Results flow up, control flows down.** A child returns its result with `pairmux report`; it
  cannot drive its parent or siblings (`E_SCOPE`). See [Agent hierarchy](./agent-hierarchy.md).

## The parent's loop

```bash
# One terminal per child, model stated explicitly
pairmux new --name api --cwd ./services/api --cmd "claude --model <model-id>"
pairmux new --name web --cwd ./web --cmd "codex --model <model-id>"
pairmux peek api            # at its prompt, intended model in the banner, no dialog left

# Task + completion signal + permission to recurse, in one line
pairmux send api --text "Implement GET /users with tests. When done run pairmux report \"<outcome; evidence; next step>\" and stop. If a part is big, start your own sub-agents the same way and fold their reports into yours." --enter

# Block on reports instead of sleeping
pairmux wait api,web --note --timeout 1800s
pairmux events --tree --wait --status awaiting-input,dead --timeout 30m

pairmux peek api            # read the report in notes, then check the evidence it names
pairmux kill api            # done: ends api and everything below it
```

A grandchild is the same loop run inside the child's terminal; its `pairmux new` lands one layer
further down, and its report goes to the child, which folds it into its own.

## Environment hygiene

A child inherits its creator's environment. Wrappers and proxies (for example `alc`) export
`ANTHROPIC_BASE_URL`, `ANTHROPIC_MODEL` and similar variables, so a child can silently run a
different model or endpoint. Pass the model explicitly, clear what you do not intend
(`--cmd "env -u ANTHROPIC_BASE_URL -u ANTHROPIC_MODEL claude --model <model-id>"`), and verify by
asking the child for its model or by listing variable names (not values) in a shell terminal:

```bash
pairmux new --name envcheck
pairmux run envcheck "env | cut -d= -f1 | grep '^ANTHROPIC_'; printenv ANTHROPIC_BASE_URL" --timeout 10s
pairmux kill envcheck
```

:::note pairmux 1.5.1 and older on tmux

New terminals received the tmux server's global environment, captured from whichever process first
started the server (for example a stale `alc --codex claude` session), rather than the caller's.
This is fixed in pairmux 1.6.0. On older versions, inspect and clear the stale variables, or restart the
server when no work is running in it (`kill-server` ends every terminal on that socket):

```bash
tmux -L pairmux show-environment -g | cut -d= -f1 | grep '^ANTHROPIC_'
tmux -L pairmux set-environment -g -u ANTHROPIC_BASE_URL
```

:::
