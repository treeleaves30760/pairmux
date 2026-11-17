package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/vt"
)

// This file makes the TUI's layout match what the terminal really draws.
//
// Most glyphs the frame is built from (● ◆ ✕ ▌ ▾ … ─ │ ╭ ╮ ╰ ╯) are East
// Asian Ambiguous: one cell in most terminals, two in one configured for CJK.
// Guessing from the locale is unreliable, so the TUI measures at startup:
// it prints each glyph at column 1 of row 2 and asks the terminal where the
// cursor went (DSR, ESC[6n → ESC[2;colR), all probes in one write and one
// short overall timeout. Row 2, not row 1: a report for row 1 (ESC[1;2R) has
// the exact shape of a modified F3 key, so only row-2 reports count as
// replies and everything else is a key.
//
// What it does with the answer is deliberately simple. The frame's geometry
// assumes one-cell glyphs in many places (border corners and fill, the box
// sides, the selection mark, fold arrows, status dots, the clip ellipsis), so
// a glyph that measured wide is not threaded through every computation; it is
// replaced by a one-cell ASCII stand-in (gl), keeping a single layout path.
// Everything else — text, hints, terminal names, preview content — is
// measured by strWidth, which counts what the terminal reported, so padding,
// truncation and mouse columns stay exact. On a terminal that answers "one
// cell" (or does not answer) nothing changes: the frame is byte-for-byte the
// one drawn without a probe.

// AmbiguousWidthEnv overrides the probe: 1 or 2 cells for the TUI's ambiguous
// glyphs, for a terminal that does not answer DSR, and for tests.
const AmbiguousWidthEnv = "PAIRMUX_TUI_AMBIGUOUS_WIDTH"

// probeGlyphs is every non-ASCII glyph the TUI's own chrome draws.
const probeGlyphs = "●◆○✕▌▾▸…─│╭╮╰╯┐┌┘└├█¹²⏎␣✉✓·—–←↑→↓↳⌨"

// probeRow is the screen row the probe prints on (see above for why not 1).
const probeRow = 2

// probeTimeout bounds the whole probe, however many glyphs it asks about.
const probeTimeout = 200 * time.Millisecond

// cellTable is what the probe learned: the measured width of each probed
// glyph that is not one cell, and whether other ambiguous characters
// (in content the TUI did not probe) should be counted as two cells.
type cellTable struct {
	wide    map[rune]bool
	ambWide bool
}

// tuiCells is set once, before the first frame is drawn, and only read after.
// Its zero value is the classic one-cell behaviour.
var tuiCells cellTable

// runeCells is how many cells r takes in this terminal.
func runeCells(r rune) int {
	w := vt.RuneWidth(r)
	if w != 1 || len(tuiCells.wide) == 0 && !tuiCells.ambWide {
		return max(w, 0)
	}
	if tuiCells.wide[r] || tuiCells.ambWide && vt.IsAmbiguous(r) {
		return 2
	}
	return 1
}

// gl returns glyph if it draws one cell per rune in this terminal, otherwise
// ascii, a stand-in of the same width as glyph has runes. Use it wherever the
// layout assumes the glyph's width instead of measuring it.
func gl(glyph, ascii string) string {
	if len(tuiCells.wide) == 0 {
		return glyph
	}
	for _, r := range glyph {
		if tuiCells.wide[r] {
			return ascii
		}
	}
	return glyph
}

// newCellTable builds the table from measured widths (rune → cells).
func newCellTable(widths map[rune]int) cellTable {
	t := cellTable{wide: map[rune]bool{}}
	amb, ambWide := 0, 0
	for r, w := range widths {
		if w == 2 {
			t.wide[r] = true
		}
		if vt.IsAmbiguous(r) {
			amb++
			if w == 2 {
				ambWide++
			}
		}
	}
	// Unprobed ambiguous characters follow the probed ones: a terminal set to
	// "ambiguous is wide" widens them all.
	t.ambWide = amb > 0 && ambWide*2 > amb
	if len(t.wide) == 0 {
		return cellTable{}
	}
	return t
}

// uniformWidths is every probe glyph at width w (the env override).
func uniformWidths(w int) map[rune]int {
	m := map[rune]int{}
	for _, r := range probeGlyphs {
		m[r] = w
	}
	return m
}

// detectCells decides the cell table at TUI startup: from the env override
// if set, else by probing the terminal. It returns any bytes read during the
// probe that were not replies — keys the human typed meanwhile — for the
// normal input path.
func detectCells(out io.Writer, in <-chan []byte) (cellTable, []byte) {
	switch strings.TrimSpace(os.Getenv(AmbiguousWidthEnv)) {
	case "1":
		return cellTable{}, nil
	case "2":
		return newCellTable(uniformWidths(2)), nil
	}
	widths, rest, ok := probeWidths(out, in, probeGlyphs, probeTimeout)
	if !ok {
		return cellTable{}, rest
	}
	return newCellTable(widths), rest
}

// probeWidths prints each glyph at column 1 of probeRow, asking for the
// cursor position after each, then erases the row. Replies are read from in
// until all have arrived or timeout passes. Any other bytes (keystrokes typed
// meanwhile) are returned in order in rest. ok is false when the terminal did
// not answer every probe sensibly; widths is then nil.
func probeWidths(out io.Writer, in <-chan []byte, glyphs string, timeout time.Duration) (widths map[rune]int, rest []byte, ok bool) {
	runes := []rune(glyphs)
	var q strings.Builder
	fmt.Fprintf(&q, "\x1b[%d;1H", probeRow)
	for _, r := range runes {
		q.WriteString("\r")
		q.WriteRune(r)
		q.WriteString("\x1b[6n")
	}
	q.WriteString("\r\x1b[2K")
	if _, err := io.WriteString(out, q.String()); err != nil {
		return nil, nil, false
	}

	var buf []byte
	var replies [][2]int
	timer := time.NewTimer(timeout)
	defer timer.Stop()
collect:
	for len(replies) < len(runes) {
		select {
		case b := <-in:
			buf = append(buf, b...)
			var got [][2]int
			got, buf = extractCPR(buf)
			replies = append(replies, got...)
			rest = append(rest, takeSettled(&buf)...)
		case <-timer.C:
			break collect
		}
	}
	rest = append(rest, buf...)
	if len(replies) != len(runes) {
		return nil, rest, false
	}
	widths = make(map[rune]int, len(runes))
	for i, rep := range replies {
		w := rep[1] - 1
		if w < 1 || w > 2 {
			return nil, rest, false
		}
		widths[runes[i]] = w
	}
	return widths, rest, true
}

// extractCPR removes every complete cursor position report for probeRow
// (ESC [ 2 ; col R) from b, returning them in order and the remaining bytes.
// A report for any other row is left in place: it is a key (ESC[1;2R is
// Shift-F3), not a reply.
func extractCPR(b []byte) (reps [][2]int, rest []byte) {
	rest = b[:0:0]
	for i := 0; i < len(b); {
		if row, col, n := parseCPR(b[i:]); n > 0 && row == probeRow {
			reps = append(reps, [2]int{row, col})
			i += n
			continue
		}
		rest = append(rest, b[i])
		i++
	}
	return reps, rest
}

// parseCPR reads one ESC [ row ; col R at the start of b; n is its length, or
// 0 when b does not start with a complete one.
func parseCPR(b []byte) (row, col, n int) {
	if len(b) < 6 || b[0] != 0x1b || b[1] != '[' {
		return 0, 0, 0
	}
	i := 2
	num := func() (int, bool) {
		start, v := i, 0
		for i < len(b) && b[i] >= '0' && b[i] <= '9' && i-start < 5 {
			v = v*10 + int(b[i]-'0')
			i++
		}
		return v, i > start
	}
	row, ok := num()
	if !ok || i >= len(b) || b[i] != ';' {
		return 0, 0, 0
	}
	i++
	col, ok = num()
	if !ok || i >= len(b) || b[i] != 'R' {
		return 0, 0, 0
	}
	return row, col, i + 1
}

// takeSettled moves the bytes of *buf that cannot be the start of a reply
// still arriving out to the caller, keeping a trailing partial ESC sequence.
func takeSettled(buf *[]byte) []byte {
	b := *buf
	keep := len(b)
	if i := lastIndexByte(b, 0x1b); i >= 0 && isCPRPrefix(b[i:]) {
		keep = i
	}
	out := append([]byte(nil), b[:keep]...)
	*buf = append(b[:0:0], b[keep:]...)
	return out
}

func lastIndexByte(b []byte, c byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// isCPRPrefix reports whether b could still grow into a reply, ESC [ 2 ; digits R.
func isCPRPrefix(b []byte) bool {
	head := fmt.Sprintf("\x1b[%d;", probeRow)
	if len(b) <= len(head) {
		return head[:len(b)] == string(b)
	}
	if string(b[:len(head)]) != head {
		return false
	}
	for _, c := range b[len(head):] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
