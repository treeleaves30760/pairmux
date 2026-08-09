# Human collaboration

pairmux terminals are real tmux panes, so a human can watch them, take one over, do something you
can't or shouldn't (type a password, make a judgement call), and hand back — all in the same live
session. You find out what the human did through **notes**.

## The three human commands

- **`pairmux watch`** — a live dashboard of every terminal: status, lock holder, current command,
  last activity. `!!` flags awaiting-input, `xx` flags dead. `Ctrl-C` quits.
- **`pairmux attach [name]`** — hand the human a live tmux client focused on `name`. They are now
  typing into the same pane you use. (They detach with tmux's `Ctrl-b d`.)
- **`pairmux note <name> <text>`** — leave a message the agent sees on its next `run`/`peek`/`wait`,
  and which resolves `wait --human`.

## The handoff loop (secret prompt)

This is the canonical pairing loop: you hit a password you must not guess, summon a human, the human
answers in the pane, and you resume knowing they helped.

**1. You hit a secret prompt.** The reply forbids guessing and points at handoff:
```json
{"schema":"pairmux.v1","ok":true,"status":"awaiting-input","terminal":"dbmigrate","mode":"hooks","output":"\nPassword: ","next":["do NOT guess or type secrets","pairmux wait dbmigrate --human --notify   # hand off to the human"]}
```

**2. You hand off and block**, firing a desktop notification:
```bash
pairmux wait dbmigrate --human --notify
```
`--notify` pops a desktop notification (`osascript` on macOS, `notify-send` on Linux —
best-effort). Your tool call is now parked. If the shell/tool client interrupts before pairmux
returns, immediately reissue the same `wait --human --notify`; never type the secret or shorten the
300-second default wait. An explicit handoff timeout must be a single valid Go duration of at least
`300s`.

**3. The human takes over the same pane and types the secret:**
```bash
pairmux attach dbmigrate            # they land in the live pane; they type the password
pairmux note dbmigrate "entered the db password"   # optional
```

**4. Your `wait` unblocks — two ways, and you must handle both.** A note is the explicit signal:
```json
{"schema":"pairmux.v1","ok":true,"status":"human-done","terminal":"dbmigrate","mode":"hooks","output":"entered the db password","next":["pairmux peek dbmigrate"]}
```
But a human who answers the prompt and walks away leaves no note at all, so the handoff also ends
the moment the terminal is moving again. **Treat this exactly like `human-done` — the human is
finished.** What you are told is that *execution resumed*, not that the command finished:
```json
{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"dbmigrate","mode":"hooks","next":["pairmux wait dbmigrate --done","pairmux peek dbmigrate"]}
```
That distinction matters. A password answered at second two can be followed by a five-minute
migration, and you should not be parked for those five minutes; if you do want the result, follow
the `next` and `wait --done` for it. When the command instead finishes without printing anything
after the answer — or had already finished before you got around to waiting — you get
`status: done` with its `exit_code` directly.

A wrong password that re-prompts does **not** end the wait: it is still awaiting input, so you stay
parked instead of bouncing back to the same handoff hint. And you are not punished for waiting late:
hand off, go do other work, and a completion that already landed returns from the next
`wait --human` immediately.

**If it times out, the human simply has not come yet.** That is not a failure and not a reason to
act on your own — the reply says so and hands you the same wait with a longer deadline:
```json
{"schema":"pairmux.v1","ok":true,"status":"timeout","terminal":"dbmigrate","mode":"hooks","next":["the human has not answered yet — do NOT type the secret","pairmux wait dbmigrate --human --notify --timeout 10m0s","pairmux peek dbmigrate"]}
```
Run it. Keep waiting.

**5. You resume** with `pairmux peek dbmigrate` and carry on. The password was never seen by,
echoed to, or logged for you: `--human` deliberately returns no `output`, because the span it would
quote is the span the human typed into.

## Notes flow both ways

A note is a general side channel, not just for handoffs. A human (or another agent) can leave context
at any time, and it surfaces in your **next** `run`/`peek`/`wait` in the `notes` field:

```bash
pairmux note build "use the staging token, not prod"
```
```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"duration_ms":101,"output":"resumed","notes":["use the staging token, not prod"]}
```

**When you see `notes`, read them and follow them** — they override your earlier assumptions. `ls`
shows a `[notes:N]` badge so a human can see what is waiting to be picked up. A note stays visible on
every `peek`/`run`/`wait` (it is not consumed by reading), so you won't miss it.

## Waiting for the human, not fighting them

While a human is typing in a pane, don't fight them for it. The discipline: **if a human has attached,
`wait` (don't `run`) until the wait resolves** — a note, or the command they unblocked finishing.
Notes are recorded in the journal and surface in the next reply; attaching itself is deliberately
just a live tmux operation and does not create an event.

`wait --human` also returns immediately if a note is *already* waiting and unseen — so the natural
"human notes, then the agent waits" ordering never drops a message.

## Subscribing to a terminal you are not driving

`wait --done` blocks until the terminal's running command finishes — or until the next one does, if
it is idle right now — and reports the `exit_code`:

```bash
pairmux wait build --done --timeout 600s
```
```json
{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"build","mode":"hooks","exit_code":0,"output":"…\nBUILD SUCCESSFUL","next":["pairmux peek build"]}
```

You do not have to be the agent that started the command, and you do not have to register anywhere:
`wait` records nothing and takes no lock, so any number of agents can hold a `--done` wait on one
terminal and every one of them wakes on the same completion. That is the whole subscription
mechanism — the journal is the broadcast. It works for a command a **human** typed into the pane
too. Shell terminals only: a `--cmd` program terminal emits no completion marks, so use `--pattern`
or `--idle` there.
