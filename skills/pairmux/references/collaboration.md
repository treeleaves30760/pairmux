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
`wait --human` blocks until a human leaves a note; `--notify` pops a desktop notification
(`osascript` on macOS, `notify-send` on Linux — best-effort). Your tool call is now parked.

**3. The human takes over the same pane, types the secret, and leaves a note:**
```bash
pairmux attach dbmigrate            # they land in the live pane; they type the password
pairmux note dbmigrate "entered the db password"
```

**4. Your `wait` unblocks the instant the note lands** — the note text is the `output`:
```json
{"schema":"pairmux.v1","ok":true,"status":"human-done","terminal":"dbmigrate","mode":"hooks","output":"entered the db password","next":["pairmux peek dbmigrate"]}
```

**5. You resume** with `pairmux peek dbmigrate` and carry on. The password was never seen by,
echoed to, or logged for you.

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
`wait` (don't `run`) until they leave a note.** Notes are recorded in the journal and surface in the
next reply; attaching itself is deliberately just a live tmux operation and does not create an event.

`wait --human` also returns immediately if a note is *already* waiting and unseen — so the natural
"human notes, then the agent waits" ordering never drops a message.
