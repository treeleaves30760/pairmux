# Driving other agents: layers, paths, scope and report

Read this when you start other coding agents (Claude Code, Codex, Gemini CLI, OpenCode, …) in
pairmux terminals, when you were started in one yourself, or when a reply says `E_SCOPE`.

## The model

Every pairmux terminal gets a layer of its own **one level down**. Whatever an agent inside it opens
with plain `pairmux new` lands there, so agents form a tree:

```text
human (no scope) ── layer L0: lead
                    lead's layer L1: api, web
                    api's layer L2: tests
```

- **You drive your own layer and everything below it.** From `lead`: `api`, `web`, `api/tests`.
- **Nothing below can drive anything above or beside it.** `api` cannot address `lead`, `web`, or
  layer L0. Such a call fails with `E_SCOPE`; that is the design, not a bug to work around.
- **The top belongs to the human.** If you run in the human's own terminal (no `PAIRMUX_SCOPE`), you
  are the top: nothing else may drive you, and you may drive the whole tree.
- **`report` is the only upward channel.** It leaves a note on your own row in your owner's layer.

Check where you stand: inside a pairmux terminal `PAIRMUX_NAME` is your own name,
`PAIRMUX_SCOPE` the highest layer you may address, and `PAIRMUX_OWNER_SOCKET` where `report` writes.
Never unset or rewrite `PAIRMUX_SCOPE`, `PAIRMUX_SOCKET` or `--socket` to escape your scope.

## Starting sub-agents (coordinator side)

```bash
pairmux new --name api --cmd "codex" --cwd ./services/api      # one terminal per worker
pairmux new --name web --cmd "claude" --cwd ./web
pairmux peek api                                               # verify it started; read its first screen
pairmux send api --text "Implement GET /users with tests. When done run: pairmux report \"<summary>\"" --enter
```

- Give each worker a precise task, its working directory, and how to signal completion — tell it to
  run `pairmux report "<summary>"` (or put that in its stop hook). A quiet TUI is not a finished turn.
- An agent CLI is a **program terminal**: drive it with `send`/`peek`/`wait --idle|--note|--pattern`,
  never `run` or `wait --done`.
- Before sending, `peek` to confirm the agent is at its input prompt (startup screens, trust dialogs,
  login prompts). Answer non-secret dialogs once; hand credentials/logins to the human
  (`wait <name> --human --notify`).

## Waiting on workers

```bash
pairmux wait api,web --note --timeout 1800s     # first worker to report ends the wait; reply names it
pairmux peek api                                # read its report in notes + its screen
```

With several workers, do not loop over `peek`: one call blocks until anything in your subtree needs
you, says which terminal, and what to run next:

```bash
pairmux events --tree --wait --status awaiting-input,dead --timeout 30m
# changed  api/tests  running → awaiting-input  →  next: pairmux peek api/tests
```

Without `--status` the first change of any kind returns (a worker starting or finishing a command
too); `--max N` collects N changes; on `timeout` repeat it with the longer deadline from `next`. Over
MCP it is the `pairmux_events` tool. Keep `wait --note` for "this worker said it is done".

Use `ls --tree` for the fleet's state at a glance (`awaiting-input` rows need attention). A worker
stuck at `awaiting-input` on a non-secret confirmation may be answered once with `send`; a secret or
judgment call goes to the human.

## Reaching deeper terminals by path

Address any descendant by path from where you stand; replies and their `next` commands keep the path:

```bash
pairmux ls --tree                         # rows carry "path": "api/tests"
pairmux peek api/tests
pairmux run api/tests "go test ./..." --timeout 30s    # only if api/tests is a shell terminal
pairmux new --name api/lint --cmd "npm run lint -- --watch"
pairmux kill api/tests
```

Prefer telling the owning worker over reaching past it: a worker does not know you typed into its
helper. Reach down directly to read (`peek`, `log`, `ls --tree`) or in an emergency (a runaway
process), then tell the worker with `pairmux note api "..."`. One `wait` can name several terminals
of a single layer only (`wait api/tests,api/lint`).

## Being a worker (inside a pairmux terminal)

```bash
pairmux new --name tests                  # your own helper terminals: your layer
pairmux run tests "pytest -q" --timeout 30s
pairmux report "GET /users done; 6 tests pass; see services/api/users.py"
```

- `report` text is read by the agent that created you. Keep it to outcome, evidence, next step.
  Never put secrets in it.
- You cannot attach, note, send or kill anything above you. If you need your coordinator, `report`
  and stop; if you need a human, use a human handoff on your own terminal's prompt.

## Ending work

- `kill <name>` ends a worker **and every layer below it** (its sub-agents too); journals are kept.
  Killing an agent mid-turn loses its unsaved work — confirm the user wants it.
- `prune` reclaims retained journals of dead terminals, including layers they used to drive.
- Humans manage the tree interactively with `pairmux ls` / `pairmux tui` (attach, create, kill,
  delete stale, note, send, read history). It is a human interface: do not launch it yourself.

## E_SCOPE

`endpoint "X" is outside this terminal's scope "Y"` — you addressed a layer above or beside yours
(a `--socket` copied from a hint, log, or instructions; or `PAIRMUX_SOCKET` changed). Run
`pairmux ls --tree` to see what you may drive, and `pairmux report "..."` to ask your owner for
anything outside it.

---

## Opt-in: pairmux in place of native sub-agents

**Not the default.** Keep using your host's own sub-agent mechanism (Claude Code's Task/Agent tool,
Codex's sub-agents, …) as usual. Switch to this recipe only when the user explicitly asks for it,
e.g. "use pairmux to build a hierarchy of sub-agents", "manage sub-agents hierarchically with
pairmux", or 「使用 pairmux 來構建階層式的管理 Subagents」. Do not suggest it unprompted.

Why users ask for it: native sub-agents are usually one level deep (a sub-agent cannot start its
own). Here every node is a full agent CLI in its own terminal, so any node can open the next layer —
a tree of arbitrary depth. The human can `attach` to any node and take over; results flow up only
through `report`; control flows only down (`E_SCOPE` above or beside).

### Parent loop

```bash
# 1. One terminal per child, with the model and flags stated explicitly (never rely on inheritance).
pairmux new --name api --cwd ./services/api --cmd "claude --model <model-id>"
pairmux new --name web --cwd ./web --cmd "codex --model <model-id>"
# 2. Verify startup: at its input prompt, banner shows the intended model, no trust/login dialog left.
pairmux peek api
# 3. Hand over the task, the completion signal, and permission to recurse — one line.
pairmux send api --text "Implement GET /users with tests. You are a pairmux sub-agent: when done run pairmux report \"<outcome; evidence; next step>\" and stop. If a part is big, start your own sub-agents the same way (pairmux new --name <n> --cmd \"<agent> --model <id>\"), wait for their reports and fold them into yours." --enter
# 4. Block on reports instead of sleeping or polling peek.
pairmux wait api,web --note --timeout 1800s
pairmux events --tree --wait --status awaiting-input,dead --timeout 30m   # who needs attention, at any depth
# 5. Read the report (notes) and check the evidence it names before trusting it.
pairmux peek api
# 6. Close finished children; kill also ends their subtree.
pairmux kill api
```

- A child's permission or trust prompt shows as `awaiting-input`: answer it only within what the
  user authorized; otherwise hand it to the human (`wait <name> --human --notify`).
- Give each child a distinct `--cwd` or file set so siblings do not edit the same files.
- Kill a child only after its report (or with the user's consent): mid-turn work is lost.

### Grandchildren

The child does the same from inside its pane; its plain `pairmux new` lands one layer down. You see
the whole tree and can read any node by path, but leave driving to its owner:

```bash
# inside api's terminal
pairmux new --name tests --cmd "claude --model <model-id>"
pairmux wait tests --note --timeout 1800s
pairmux report "GET /users done; tests (via api/tests) pass; see services/api/users.py"
# at the top
pairmux ls --tree                          # api, api/tests, web …
pairmux peek api/tests
```

### Environment hygiene

A child inherits its creator's environment. Proxies and wrappers (e.g. `alc`) export
`ANTHROPIC_BASE_URL`, `ANTHROPIC_MODEL`, and similar, so a child may silently run another model or
endpoint. Always pass the model explicitly, clear variables you do not intend in `--cmd` (POSIX:
`--cmd "env -u ANTHROPIC_BASE_URL -u ANTHROPIC_MODEL claude --model <model-id>"`), and verify: ask the
child to state its model, or check names (never secret values) in a plain shell terminal of the
same layer:

```bash
pairmux new --name envcheck
pairmux run envcheck "env | cut -d= -f1 | grep '^ANTHROPIC_'; printenv ANTHROPIC_BASE_URL" --timeout 10s
pairmux kill envcheck
```

Unexpected names there, with pairmux 1.5.1 or older on tmux: see
[troubleshooting.md](troubleshooting.md#a-child-agent-runs-the-wrong-model-or-endpoint).
