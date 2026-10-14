// Package vt is the virtual terminal behind pairmux's built-in multiplexer
// server. The server owns each program's PTY (openpty on Unix, ConPTY on
// Windows) and runs everything the program writes through a Screen, which
// keeps the screen state the program has drawn. That state serves three
// consumers:
//
//  1. Capture: a plain-text snapshot with the semantics of
//     `tmux capture-pane -p` (what agents read with `pairmux peek --screen`).
//  2. Render: a full repaint for a human whose terminal attaches mid-session.
//  3. Feed's forward stream: after attaching and rendering, the server relays
//     the program's raw output to the human's terminal (dtach/shpool style),
//     minus the few sequences that must not reach it. Feed also returns the
//     answers to the program's device queries, which the server writes back
//     to the program's input whether or not anyone is attached; fish, vim,
//     PSReadLine and ConPTY all query the terminal and can stall without an
//     answer.
//
// A Screen is not safe for concurrent use; the server serializes access.
//
// # Emulation
//
// The emulator implements the xterm-compatible subset that shells, REPLs,
// pagers, full-screen TUIs, PSReadLine and ConPTY output rely on: UTF-8 text
// (decoded across Feed boundaries, invalid bytes shown as U+FFFD, 8-bit C1
// controls not interpreted), wide and zero-width characters (see RuneWidth),
// C0 controls, DEC Special Graphics line drawing via G0–G3 and SI/SO, cursor
// movement and addressing (origin mode included), erasing, insertion and
// deletion of characters and lines, scroll regions, tab stops, SGR, DECSC and
// DECRC, the alternate screen (?47, ?1047, ?1049), the modes in Modes, and
// xterm's pending-wrap rule: a glyph written in the last column leaves the
// cursor there with a wrap pending, which the next glyph performs and any
// cursor movement or erase cancels.
//
// Zero-width code points attach to the cell left of the cursor (the cursor
// cell itself while a wrap is pending), as in tmux: combining marks, ZWJ,
// variation selectors and other format characters. A combining mark in column
// 0 has nothing to attach to and is dropped. Like tmux 3.7, a VS16 (U+FE0F)
// widens a narrow glyph to two cells, a non-ASCII character following a ZWJ
// joins the ZWJ's cell (so an emoji ZWJ sequence occupies one glyph), and the
// second regional indicator of a pair joins the first as a two-cell flag.
// Writing or erasing either half of a wide glyph clears the other half.
//
// Lines that scroll off the top of the main screen while the scroll region
// starts at the top row are kept as history, up to the history limit (the
// oldest line is dropped first). The alternate screen has no history. Like
// tmux's default scroll-on-clear, clearing the whole main screen (ED 2, or ED 0
// from the home position) first moves the rows in use into the history; ED 3
// clears the history.
//
// Resize does not reflow text. See Screen.Resize for the details.
//
// # Capture
//
// Capture(n) prints every visible row, each followed by "\n" with trailing
// spaces trimmed; wide glyphs print once, blank cells print as spaces and
// combining marks follow their base. With n > 0 on the main screen the last n
// history lines come first. On the alternate screen only the alternate screen
// is printed. This matches `tmux capture-pane -p [-S -n]` (tmux 3.7), which a
// differential test checks against a real tmux, except where noted:
//
//   - A tab over empty cells prints as the spaces it skipped (tmux 3.5+
//     prints '\t' for its tab cells).
//   - DEC Special Graphics print as their Unicode glyphs (tmux prints the
//     ASCII letters, "lqqk" for "┌──┐").
//   - The main screen's history is not printed while the alternate screen is
//     active.
//   - Only a scroll region starting at the top row feeds the history, as in
//     xterm; tmux also saves rows scrolled out of lower regions.
//   - The history holds exactly the history limit; tmux trims 10% at a time.
//   - Where tmux and xterm disagree, Screen follows xterm: a pending wrap is
//     cancelled by LF, BS, cursor movement and erasure (EL 0 then erases the
//     last-column glyph) and survives DECSC/DECRC; backspace in column 0 never
//     moves to the previous row; DECSTBM treats a bottom margin of 0 as the
//     last row; CHT, HPR, VPR, DECST8C and ?1048 (which tmux ignores) are
//     implemented and REP may wrap; ICH, IL and DL with counts larger than
//     the space left clear what they should (tmux leaves stale cells); ?47h
//     does not clear the alternate screen; erasing or shifting half of a wide
//     glyph clears the other half (tmux leaves an orphaned half).
//   - Spacing combining marks (Mc) are one cell wide, as in glibc's wcwidth;
//     tmux linked with utf8proc gives them no width. A few emoji modifier
//     bases with text presentation (U+261D, U+26F9, U+270C, U+270D, U+1F3CB,
//     U+1F3CC, U+1F574, U+1F575, U+1F590) are narrow per EastAsianWidth.txt;
//     tmux shows them wide. A Hangul medial vowel or final consonant with no
//     preceding syllable to join is zero-width (tmux gives it a cell), and
//     U+3164 HANGUL FILLER is printed as a wide glyph (tmux drops it).
//
// # Forward stream
//
// Feed's forward output is the input, byte for byte and in order, except for
// the sequences listed here. It only ever contains complete sequences: a
// sequence or UTF-8 character cut off at the end of one Feed is held and
// emitted, whole, by the Feed that completes it. OSC, DCS, SOS, PM and APC
// strings are buffered for inspection up to 64 KiB; a longer string is
// flushed unchanged once it crosses that size and the rest of it passes
// through unfiltered as it arrives. C0 controls embedded in an escape
// sequence are executed and forwarded immediately, ahead of the held
// sequence, which a terminal treats the same way.
//
// Device queries are removed and answered in Feed's reply output:
//
//	CSI 5 n                  ESC [ 0 n
//	CSI 6 n                  ESC [ row ; col R  (1-based; row relative to the
//	                         scroll region when origin mode is set)
//	CSI ? 6 n                ESC [ ? row ; col R
//	CSI c, CSI 0 c, ESC Z    ESC [ ? 1 ; 2 c
//	CSI > c, CSI > 0 c       ESC [ > 0 ; 10 ; 1 c
//	CSI > q, CSI > 0 q       ESC P > | pairmux ESC \
//	CSI ? Ps $ p             ESC [ ? Ps ; Pm $ y  (Pm 1 set, 2 reset, 0 unknown)
//	CSI Ps $ p               ESC [ Ps ; Pm $ y    (only IRM, mode 4, is known)
//	CSI 11 t                 ESC [ 1 t
//	CSI 13 t                 ESC [ 3 ; 0 ; 0 t
//	CSI 14 t                 ESC [ 4 ; rows*16 ; cols*8 t
//	CSI 15 t                 ESC [ 5 ; rows*16 ; cols*8 t
//	CSI 16 t                 ESC [ 6 ; 16 ; 8 t
//	CSI 18 t                 ESC [ 8 ; rows ; cols t
//	CSI 19 t                 ESC [ 9 ; rows ; cols t
//	OSC 10 ; ?               OSC 10 ; rgb:c0c0/c0c0/c0c0
//	OSC 11 ; ?               OSC 11 ; rgb:0000/0000/0000
//	OSC 12 ; ?               OSC 12 ; rgb:c0c0/c0c0/c0c0
//	                         (with the query's terminator, BEL or ST; a chained
//	                         query such as OSC 10;?;? is answered per color)
//	DCS $ q … ST (DECRQSS)   ESC P 0 $ r ESC \
//	DCS + q … ST (XTGETTCAP) ESC P 0 + r ESC \
//
// These are removed without an answer: ENQ; DA3 (CSI = c) and other DA
// variants; other DSR requests (CSI Ps n, CSI ? Ps n); DECREQTPARM
// (CSI x); XTQMODKEYS (CSI ? Ps m); XTSMGRAPHICS (CSI ? … S); every other
// XTWINOPS (CSI … t) except the title stack operations 22 and 23, which pass,
// so that nothing reports, moves, resizes or iconifies the human's window;
// OSC 4 palette queries (4;n;?); OSC 52 clipboard reads (52;c;?), which would
// make the human's terminal type the clipboard into the program; and kitty
// graphics queries (APC G … a=q …), whose late answer would arrive as typed
// garbage after the program, which already received our DA1 answer, stopped
// waiting.
//
// Mode changes that would break the attached terminal are removed: the kitty
// keyboard protocol (CSI ? u query, CSI > … u push, CSI < … u pop,
// CSI = … u set), so the human's keys keep arriving in the legacy encodings
// programs fall back to; Win32 input mode (CSI ? 9001 h/l), which ConPTY
// requests; and DECCOLM (CSI ? 3 h/l), which would resize and clear the real
// terminal (the emulator ignores it too). A plain CSI u (restore cursor) is
// not affected.
//
// The alternate screen is emulated, never forwarded: the attaching client
// keeps the human's terminal on its own alternate screen for the whole
// attach. A DECSET/DECRST containing ?47, ?1047 or ?1049 is applied to the
// emulator and removed; any other modes in the same sequence are re-emitted
// as their own DECSET/DECRST in their original order around it, and wherever
// the active buffer actually changes (or ?1049l restores a different cursor)
// a full Render of the newly active buffer is inserted at that position in
// the stream. RIS (ESC c), which would also take the human's terminal off its
// alternate screen, is likewise applied and replaced by a Render.
//
// Everything else, including text, SGR, cursor movement, other OSC (title,
// hyperlinks, OSC 133/7779 shell integration, OSC 52 writes), DCS (sixel,
// tmux passthrough) and mode changes, passes through unchanged.
package vt
