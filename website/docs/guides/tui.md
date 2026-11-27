---
title: The TUI
description: "Watch and steer a tree of AI agents from one screen with pairmux tui: boxes, status glyphs, keys, mouse, the activity view and answering a prompt handoff."
---

# The TUI

`pairmux tui` is the console where you watch and steer every terminal below you: the agents you
started, the workers they started, and those workers' sub-agents. A bare `pairmux ls` (or `list`) at
your own interactive terminal opens it too. Agents, pipes, `--json`, and anything inside a pairmux
terminal still get the plain listing, so an agent cannot get stuck in the TUI.

![The pairmux TUI: the agent tree with live status on the left, the selected agent's screen on the right](/img/tui-overview.svg)

## Reading the screen

The layout follows [btop](https://github.com/aristocratos/btop). Every region is a box. Its name and
controls sit in its border, and the key for each control is highlighted, so the screen doubles as a
key reference. Every label in a border is also a button.

| region | what it shows |
|---|---|
| **header** | the endpoint and backend (`pmxdemo · tmux`), a count per status, the clock |
| **terminals** box | the whole tree: fold arrow, status glyph, name, a short state word (`run`, `input`, `idle`, `dead`), time since last output, and the start of the running command in whatever room is left. `✉N` marks notes its agent has not read, and `+N` marks the terminals in a folded subtree |
| **preview** box | the selected terminal: its running command in full on the first line (cut with `…` when it does not fit), then when and how it was created, then **¹screen** (what is on it now) or **²activity** (its history of commands, exit codes and notes) |
| **footer** | what to do about the selected terminal — or the result of your last action — and the global buttons |

Status glyphs: `●` running, `◆` needs input, `○` idle, `✕` dead. When you select a terminal that
needs input, the footer turns yellow and says how to answer it.

On a terminal at least 110 columns wide the two boxes sit side by side, and the list is the smaller
one: about 38% of the width (42 to 84 columns), so the preview gets the room. A narrower terminal
stacks them. A name too long for its column ends in `…`; on the selected row it scrolls instead,
after a short pause, so you can read all of it.

### Full-screen programs fit the preview

A full-screen program such as Claude Code, Codex, vim or htop draws for the size of its terminal.
Terminals are 220x50, so their agents' output does not wrap, and cut into a preview box such a
program shows only its top-left corner. So while one is selected, the TUI resizes its terminal to
the preview box and the program redraws to fit. Attaching gives it your whole terminal, as always,
and coming back fits it to the preview again. Moving on, or quitting, gives it back its size.

Only terminals that run a full-screen program are resized:

- any terminal on the alternate screen (vim, less, htop, most TUIs);
- a program terminal (`pairmux new --cmd claude`) whose program reads raw keys, hides the cursor
  or asks for the mouse, which is how Claude Code and Codex draw inline;
- a shell terminal whose running command reads raw keys and hides the cursor or wants the mouse,
  such as `claude` typed at the shell.

A shell at its prompt or running a build is never resized, so moving the selection never interrupts
an agent's command, and nothing is ever typed into a terminal. A terminal that someone is attached
to keeps their size. Nothing is resized until the selection has rested for a moment, or while the
preview is smaller than 40x10 or shows the activity view. If the TUI is killed without cleaning up,
the built-in backend gives the terminal its size back after about 20 seconds; on tmux, the next
`pairmux tui` or `pairmux attach` does.

### Terminals that draw symbols double-width

Many of these glyphs (`●` `◆` `─` `│` `╭` `…`) are East Asian *Ambiguous* characters. A terminal set
to draw them two cells wide, as CJK setups often are, pushes the boxes out of line and moves every
click. So at startup the TUI asks the terminal how wide it draws each glyph, with a cursor position
query that waits up to about 200 ms. It draws any glyph that comes back two cells wide as ASCII
instead: `+` `-` `|` for the boxes, `[label]` for the border buttons, `*` running, `!` needs input,
`o` idle, `x` dead, `>` for the selection and `v`/`>` for fold arrows. Other text, such as CJK names
or output, is measured to match and keeps its place.

A terminal that does not answer the query keeps the usual glyphs. If yours draws them two cells wide
but does not answer, set `PAIRMUX_TUI_AMBIGUOUS_WIDTH` yourself and the TUI skips the query:

- `PAIRMUX_TUI_AMBIGUOUS_WIDTH=1` draws the usual glyphs.
- `PAIRMUX_TUI_AMBIGUOUS_WIDTH=2` draws the ASCII set.

Any other value is ignored, and the TUI asks the terminal.

## Keys and buttons

| key | button | action |
|---|---|---|
| `↑` `↓` / `j` `k`, `PgUp` `PgDn`, `g` `G` | wheel over the list | move |
| `⏎` / `a` | `⏎ attach`, or click a selected row again | attach; detach with `Ctrl-b d` and you are back |
| `n` | `new` | new terminal (name, command such as `claude` or `codex`, directory, layer) at the top |
| `c` | `child` | new terminal under the selected one — a sub-agent of it |
| `d` | `delete` | kill a live terminal (and everything below it; journals kept) or delete a dead one's journal |
| `x` | `x prune dead` | delete the journals of every dead terminal in the tree |
| `space` `←` `→` | `␣ fold`, or click a `▾`/`▸` | fold / unfold a terminal's sub-terminals |
| `/` | `/ filter` | filter by path, command or status; `esc` clears |
| `1` `2` / `tab` | `¹screen` `²activity` | switch the preview |
| `o` | `open` | the preview full screen and scrollable — screen becomes the whole retained output history |
| `i` | `input` | send one line of input (never a secret — attach for that) |
| `m` | `m note` | leave a note the terminal's agent sees in its next reply |
| `:` | `: command` | run any pairmux command and read its output here |
| `r`, `?`, `q` | `refresh`, `? help`, `quit` | refresh now, show the key list, quit |

A click does what its key does. The buttons are the keys, not a second set of actions. Destructive
actions ask first, in a dialog whose `yes` and `no` are also buttons.

Inside tmux, attaching opens a nested tmux client. Detach it with `Ctrl-b Ctrl-b d`. The TUI turns
mouse reporting off while you are attached, so the pane receives your mouse, and turns it back on
when you return.

## Answering a prompt an agent handed you

An agent that reaches a password or passphrase prompt must not type it. It hands off with
`pairmux wait <name> --human --notify`, and the terminal shows up as **needs input**:

![The TUI with deploy selected: it needs input, the preview shows a sudo password prompt, and the footer says to attach and answer it](/img/tui-handoff.svg)

Press `⏎` (or double-click the row), type the answer into the real program, and detach with
`Ctrl-b d`. The agent's wait ends as soon as the prompt is answered, and the agent never sees what
you typed. Back in the TUI, press `m` to leave a note that tells the agent what you did. See
[Human collaboration](./human-collaboration.md) for the agent's side.

## What a terminal has been doing

`2` (or `tab`) switches the preview to **activity**. It shows what the terminal is and drives and
where its journal lives, then lists every command with its exit code and duration, every note and
whether its agent has read it, and every input event sent to the terminal. `o` opens it full screen.

![The activity view for lead: its path, status and layer, then each command with exit code and duration, and a note its agent has not read yet](/img/tui-activity.svg)

## Running pairmux commands in place

`:` runs any pairmux command and shows its readable output in a scrollable reader, not in
scrollback that the next redraw would wipe. `{}` stands for the selected path (`log {} --cmd 3`,
`peek {}`), and `↑` recalls the last command. A slow `run` or `wait` runs in the background, so the
tree keeps updating. Read-only commands such as `ls`, `peek`, `log` and `doctor` rerun with `r`.

![The reader showing pairmux ls --tree run from inside the TUI, with page and rerun buttons in its border](/img/tui-command.svg)

The TUI refuses commands that take over the terminal or never end (`attach`, `watch`, `tui`,
`events`, `mcp serve` and `update`) and says what to do instead.

## Limits

- The TUI needs an interactive terminal. On Windows that means Windows Terminal or a Windows 10
  1809+ console (conhost) with VT input. In an older console `pairmux tui` fails with `E_BAD_ARGS`
  and a bare `pairmux ls` prints the table; use `pairmux watch` and `pairmux attach` there. A Git
  Bash (mintty) window is not a console to Windows, so `ls` prints the table in it.
- In a Windows console with Quick Edit on, mouse clicks select text and the TUI receives none. The
  keyboard still works; turn Quick Edit off in the console's properties to get clicks.
- `NO_COLOR` turns color off. The layout and buttons stay.
- The TUI reads the same state as the CLI on a timer (`--interval`, default 1s). A program that
  needs the same live picture should subscribe to [`pairmux events`](../integrations.md) instead of
  scraping the TUI.
