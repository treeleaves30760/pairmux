package vt

import "unicode/utf8"

// maxDim bounds a Screen's columns and rows. Real terminals are far smaller;
// the bound only keeps a buggy or hostile resize request from allocating
// gigabytes of cells.
const maxDim = 4096

// maxComb bounds the bytes of zero-width code points attached to one cell, so
// an endless run of combining marks cannot grow a cell without limit. tmux
// applies a similar per-cell cap.
const maxComb = 32

// color is a packed SGR color: the top byte selects the kind, the low bits
// hold a palette index or a 24-bit RGB value. The zero value is the terminal's
// default color.
type color uint32

const (
	colorDefault color = 0
	colorIndexed color = 1 << 24 // low 8 bits: palette index 0–255
	colorRGB     color = 2 << 24 // low 24 bits: 0xRRGGBB
	colorKind    color = 0xff << 24
)

// Text attribute flags carried by attr.flags.
const (
	attrBold uint16 = 1 << iota
	attrDim
	attrItalic
	attrBlink
	attrReverse
	attrInvisible
	attrStrike
	attrOverline
)

// attr is the graphic rendition of a cell (and of the pen that writes cells).
// It is comparable so rendering can detect changes with ==.
type attr struct {
	fg, bg, ulc color
	flags       uint16
	ul          uint8 // underline style: 0 none, 1 single, 2 double, 3 curly, 4 dotted, 5 dashed
}

// Cell flags. A wide (two-column) glyph is stored as a cellWide lead cell
// followed by a cellPad cell; the pair is always created and destroyed
// together so no half of a wide glyph can survive on its own.
const (
	cellWide uint8 = 1 << iota // left half of a two-cell glyph
	cellPad                    // right half of a two-cell glyph; holds no text
)

// cell is one character position. The zero value is a blank, default-colored,
// never-written cell, which capture shows as a space.
type cell struct {
	r     rune   // base code point; 0 means blank
	flags uint8  // cellWide / cellPad
	a     attr   // graphic rendition
	comb  string // zero-width code points attached to r (UTF-8), usually ""
}

// line is one row of a buffer. used mirrors tmux's "cellused != 0": it is set
// when a glyph is written and cleared only when the whole row is erased. tmux
// uses it to decide which rows a full-screen clear pushes into history
// (scroll-on-clear), so tracking it keeps Capture's history identical.
type line struct {
	cells []cell
	used  bool
}

// buffer is a grid of rows: the main screen or the alternate screen.
type buffer struct {
	lines []line
}

func newBuffer(cols, rows int) *buffer {
	b := &buffer{lines: make([]line, rows)}
	for i := range b.lines {
		b.lines[i].cells = make([]cell, cols)
	}
	return b
}

// savedCursor is the state DECSC (ESC 7, CSI s, ?1048h, ?1049h) records and
// DECRC restores. xterm keeps one slot per buffer, so the main screen's saved
// cursor survives a full-screen program saving its own on the alternate screen.
type savedCursor struct {
	valid       bool
	x, y        int
	pendingWrap bool
	pen         attr
	origin      bool
	g           [4]byte
	gl          int
}

// Modes is a snapshot of the terminal modes a program has set. The attach
// path uses it to decide how to translate a human's keystrokes (cursor key and
// keypad modes, bracketed paste, mouse reporting) and Render restores it on
// the human's terminal.
type Modes struct {
	AppCursor      bool // DECCKM ?1
	AppKeypad      bool // DECKPAM ESC = / DECKPNM ESC > (also DECNKM ?66)
	BracketedPaste bool // ?2004
	AltScreen      bool // currently on alternate screen
	CursorVisible  bool // ?25
	FocusEvents    bool // ?1004
	MouseTracking  int  // 0 off, else last of 9/1000/1002/1003 enabled
	MouseEncoding  int  // 0 default, 1005, 1006, 1015 (or 1016, SGR-pixels)
	Autowrap       bool // ?7, default true
	Insert         bool // IRM CSI 4 h
	OriginMode     bool // ?6
}

// Screen is a virtual terminal: it consumes the byte stream a program writes
// to its PTY and maintains the visible screen, the scrollback history and the
// terminal modes. It is not safe for concurrent use; callers serialize access.
type Screen struct {
	cols, rows int

	main, alt *buffer
	buf       *buffer // the active buffer: main or alt
	altActive bool

	hist history
	// hscrolled counts history lines that arrived by scrolling (or by a
	// shrinking resize) since history was last cleared or a clear pushed the
	// screen into it. Like tmux, only those are pulled back into view when the
	// screen grows taller.
	hscrolled int

	// Cursor. cx is always within [0, cols); a glyph written in the last
	// column leaves cx there with pendingWrap set (xterm's DECAWM "last column
	// flag"), and the next printable character wraps first.
	cx, cy      int
	pendingWrap bool
	pen         attr

	// Character sets: designations of G0–G3 ('B' ASCII or '0' DEC special
	// graphics) and which one is invoked into GL by SI/SO/LS2/LS3.
	g  [4]byte
	gl int

	saved [2]savedCursor // DECSC slots: [0] main buffer, [1] alternate buffer

	top, bottom int    // scroll region, inclusive rows
	tabs        []bool // tab stop at column i

	modes       Modes
	cursorStyle int // DECSCUSR Ps (0 = terminal default)
	title       string

	// lastRune/lastWidth record the last printed glyph for REP (CSI b); 0 when
	// the previous action was not a printable character.
	lastRune  rune
	lastWidth int

	// Parser state; see parser.go.
	state       pstate
	seq         []byte // raw bytes of the escape sequence in progress
	strKind     byte   // introducer of the string in progress: ']', 'P', 'X', '^' or '_'
	strOverflow bool   // the string outgrew maxString and is being passed through
	csiOverflow bool   // the CSI outgrew maxCSI and is being passed through
	ubuf        [utf8.UTFMax]byte
	un          int // bytes of an incomplete UTF-8 sequence held in ubuf

	out, reply  []byte // Feed's results under construction
	scratch     []line
	textScratch []byte
}

// New returns a Screen of the given size whose main screen keeps at most
// historyLimit lines of scrollback. Sizes are clamped to at least 1 (and at
// most 4096); a negative historyLimit means no history.
func New(cols, rows, historyLimit int) *Screen {
	cols, rows = clampSize(cols, rows)
	if historyLimit < 0 {
		historyLimit = 0
	}
	s := &Screen{cols: cols, rows: rows}
	s.hist.limit = historyLimit
	s.main = newBuffer(cols, rows)
	s.alt = newBuffer(cols, rows)
	s.buf = s.main
	s.resetState()
	return s
}

func clampSize(cols, rows int) (int, int) {
	return clampInt(cols, 1, maxDim), clampInt(rows, 1, maxDim)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// resetState puts cursor, pen, modes, charsets, tabs and scroll region into
// their power-on state. It does not touch buffer contents or history.
func (s *Screen) resetState() {
	s.cx, s.cy = 0, 0
	s.pendingWrap = false
	s.pen = attr{}
	s.g = [4]byte{'B', 'B', 'B', 'B'}
	s.gl = 0
	s.saved = [2]savedCursor{}
	s.top, s.bottom = 0, s.rows-1
	s.modes = Modes{Autowrap: true, CursorVisible: true}
	s.cursorStyle = 0
	s.lastRune, s.lastWidth = 0, 0
	s.resetTabs()
}

// Size returns the screen dimensions.
func (s *Screen) Size() (cols, rows int) { return s.cols, s.rows }

// Cursor returns the 0-based cursor position within the visible screen. After
// a glyph is written in the last column the cursor reports that column (the
// pending wrap happens only when the next glyph arrives), as in xterm.
func (s *Screen) Cursor() (col, row int) { return s.cx, s.cy }

// Title returns the last window title set with OSC 0 or OSC 2.
func (s *Screen) Title() string { return s.title }

// Modes returns the current terminal modes.
func (s *Screen) Modes() Modes {
	m := s.modes
	m.AltScreen = s.altActive
	return m
}

// Capture returns a plain-text snapshot with the semantics of
// `tmux capture-pane -p` (no -e, no -J): every visible row, each terminated by
// "\n" with trailing spaces trimmed, wide glyphs once (their right half emits
// nothing), blank cells as spaces and combining marks after their base. When
// historyLines > 0 and the main screen is active, the last
// min(historyLines, history length) scrollback lines come first. The alternate
// screen is captured alone, without history.
func (s *Screen) Capture(historyLines int) string {
	var b []byte
	if historyLines > 0 && !s.altActive {
		n := s.hist.len()
		start := n - historyLines
		if start < 0 {
			start = 0
		}
		for i := start; i < n; i++ {
			b = append(b, s.hist.at(i)...)
			b = append(b, '\n')
		}
	}
	for _, l := range s.buf.lines {
		b = appendLineText(b, l.cells)
	}
	return string(b)
}

// appendLineText appends the text of one row plus "\n", trimming trailing
// spaces the way tmux does (including spaces a program wrote explicitly).
func appendLineText(b []byte, cells []cell) []byte {
	n := len(cells)
	for n > 0 && (cells[n-1].r == 0 || cells[n-1].r == ' ') && cells[n-1].comb == "" {
		n-- // trailing blanks would be trimmed anyway
	}
	cells = cells[:n]
	for i := range cells {
		c := &cells[i]
		switch {
		case c.flags&cellPad != 0:
		case c.r == 0:
			b = append(b, ' ')
		default:
			b = utf8.AppendRune(b, c.r)
			b = append(b, c.comb...)
		}
	}
	end := len(b)
	for end > 0 && b[end-1] == ' ' {
		end--
	}
	// Never trim into the previous line: rows before this one end in '\n'.
	return append(b[:end], '\n')
}

// pushHistory saves a row into the history. History is only ever read back
// as text (by Capture, and when a taller resize pulls lines back into view),
// so it is stored as the row's capture text: a few dozen bytes per line
// instead of a cell array with attributes.
func (s *Screen) pushHistory(cells []cell) {
	b := appendLineText(s.textScratch[:0], cells)
	s.hist.push(string(b[:len(b)-1]))
	s.textScratch = b[:0]
}

// cellsFromText rebuilds a row from history text by printing it on a scratch
// one-row screen without autowrap, so widths and joins follow the same rules
// as live output. Attributes are not kept in the history, so a pulled-back
// row comes back in the default rendition.
func (s *Screen) cellsFromText(text string) []cell {
	t := New(s.cols, 1, 0)
	t.modes.Autowrap = false
	t.Feed([]byte(text))
	return t.main.lines[0].cells
}

// ---------------------------------------------------------------------------
// Cell-level primitives

// blank returns the cell an erase leaves behind: no glyph, default attributes
// except the current background color (xterm's background color erase).
func (s *Screen) blank() cell { return cell{a: attr{bg: s.pen.bg}} }

// splitWide breaks up a wide glyph that occupies column x of cells (either
// half), replacing both halves with default blanks. Writing over or erasing
// half of a wide glyph must not leave the other half behind.
func splitWide(cells []cell, x int) {
	if x < 0 || x >= len(cells) {
		return
	}
	switch f := cells[x].flags; {
	case f&cellPad != 0:
		if x > 0 && cells[x-1].flags&cellWide != 0 {
			cells[x-1] = cell{}
		}
		cells[x] = cell{}
	case f&cellWide != 0:
		if x+1 < len(cells) && cells[x+1].flags&cellPad != 0 {
			cells[x+1] = cell{}
		}
		cells[x] = cell{}
	}
}

// printGlyph writes a printable code point of width w (1 or 2) at the cursor,
// with xterm's autowrap and insert-mode semantics.
func (s *Screen) printGlyph(r rune, w int) {
	if w == 2 && s.cols < 2 {
		return // can never fit
	}
	if s.pendingWrap {
		s.pendingWrap = false
		s.cx = 0
		s.index()
	}
	if w == 2 && s.cx == s.cols-1 {
		if !s.modes.Autowrap {
			// A wide glyph that does not fit in the last column is dropped,
			// as tmux does; there is nowhere to put it without wrapping.
			return
		}
		// Wrap first, leaving the last column as it was (xterm, tmux).
		s.cx = 0
		s.index()
	}
	ln := &s.buf.lines[s.cy]
	cells := ln.cells
	x := s.cx
	if s.modes.Insert {
		s.insertCells(cells, x, w)
	}
	if cells[x].flags != 0 {
		splitWide(cells, x)
	}
	if w == 2 {
		if cells[x+1].flags != 0 {
			splitWide(cells, x+1)
		}
		cells[x] = cell{r: r, flags: cellWide, a: s.pen}
		cells[x+1] = cell{flags: cellPad, a: s.pen}
	} else {
		cells[x] = cell{r: r, a: s.pen}
	}
	ln.used = true
	s.lastRune, s.lastWidth = r, w
	if x+w < s.cols {
		s.cx = x + w
	} else {
		s.cx = s.cols - 1
		s.pendingWrap = s.modes.Autowrap
	}
}

// prevCellX returns the column of the cell a zero-width code point combines
// with, or -1 if there is none. This mirrors tmux's screen_write_combine: the
// cell left of the cursor (the cursor cell itself while a wrap is pending),
// stepping over the right half of a wide glyph. Combining is refused when the
// cursor sits on the right half of a wide glyph or in column 0.
func (s *Screen) prevCellX() int {
	vx := s.cx
	if s.pendingWrap {
		vx = s.cols
	}
	if vx == 0 {
		return -1
	}
	cells := s.buf.lines[s.cy].cells
	x := vx - 1
	if cells[x].flags&cellPad != 0 {
		if vx == 1 || cells[x-1].flags&cellWide == 0 {
			return -1
		}
		return x - 1
	}
	if cells[x].flags&cellWide != 0 {
		return -1
	}
	return x
}

// combine attaches a zero-width code point to the previous cell. A VS16
// (emoji presentation selector) additionally widens a narrow base to two
// cells, matching tmux 3.4+'s default variation-selector-always-wide and the
// rendering of modern terminals. A combining mark with nothing to combine
// with (column 0) is discarded, as tmux does.
func (s *Screen) combine(r rune) {
	x := s.prevCellX()
	if x < 0 {
		return
	}
	ln := &s.buf.lines[s.cy]
	c := &ln.cells[x]
	if len(c.comb)+utf8.RuneLen(r) > maxComb {
		return
	}
	if c.r == 0 {
		c.r = ' '
	}
	c.comb += string(r)
	ln.used = true
	if r == vs16 && c.flags&cellWide == 0 {
		s.widen(x)
	}
}

// widen turns the narrow glyph at column x of the cursor row into a wide one
// by claiming column x+1 as its right half, and moves the cursor one column
// right of where it was. A glyph in the last column cannot widen.
func (s *Screen) widen(x int) {
	if x+1 >= s.cols {
		return
	}
	cells := s.buf.lines[s.cy].cells
	if cells[x+1].flags != 0 {
		splitWide(cells, x+1)
	}
	cells[x].flags = cellWide
	cells[x+1] = cell{flags: cellPad, a: cells[x].a}
	nx := s.cx + 1
	if s.pendingWrap {
		nx = s.cols + 1
	}
	if nx < s.cols {
		s.cx = nx
		s.pendingWrap = false
	} else {
		s.cx = s.cols - 1
		s.pendingWrap = s.modes.Autowrap
	}
}

// joinPrev handles the two cases where a printable (non-zero-width) code
// point belongs to the previous cell rather than starting a new one, as in
// tmux 3.7: a non-ASCII character following a ZWJ (emoji ZWJ sequences such
// as a family emoji render as one glyph) and the second regional indicator of
// a flag pair (which widens the pair to two cells). It reports whether r was
// consumed.
func (s *Screen) joinPrev(r rune) bool {
	x := s.prevCellX()
	if x < 0 {
		return false
	}
	ln := &s.buf.lines[s.cy]
	c := &ln.cells[x]
	switch {
	case len(c.comb) >= 3 && c.comb[len(c.comb)-3:] == "\u200d":
		if len(c.comb)+utf8.RuneLen(r) <= maxComb {
			c.comb += string(r)
		}
		return true
	case isRegionalIndicator(r) && isRegionalIndicator(c.r) && c.comb == "" && c.flags&cellWide == 0:
		c.comb = string(r)
		s.widen(x)
		return true
	}
	return false
}

// printRune prints a decoded non-ASCII code point.
func (s *Screen) printRune(r rune) {
	if r < 0xA0 {
		return // C1 controls are not interpreted in UTF-8 mode, nor shown
	}
	w := RuneWidth(r)
	if w == 0 {
		s.combine(r)
		s.lastRune = 0
		return
	}
	if s.joinPrev(r) {
		s.lastRune = 0
		return
	}
	s.printGlyph(r, w)
}

// insertCells shifts cells[x:] right by n (ICH and insert mode), discarding
// what falls off the right edge and leaving blanks at x..x+n-1.
func (s *Screen) insertCells(cells []cell, x, n int) {
	cols := len(cells)
	if n > cols-x {
		n = cols - x
	}
	if n <= 0 {
		return
	}
	if cells[x].flags&cellPad != 0 {
		splitWide(cells, x)
	}
	copy(cells[x+n:], cells[x:cols-n])
	b := s.blank()
	for i := x; i < x+n; i++ {
		cells[i] = b
	}
	if cells[cols-1].flags&cellWide != 0 {
		cells[cols-1] = cell{} // its right half was pushed off the edge
	}
}

// deleteCells removes n cells at x (DCH), shifting the rest left and filling
// the right edge with blanks.
func (s *Screen) deleteCells(cells []cell, x, n int) {
	cols := len(cells)
	if n > cols-x {
		n = cols - x
	}
	if n <= 0 {
		return
	}
	if cells[x].flags&cellPad != 0 {
		splitWide(cells, x)
	}
	if x+n < cols && cells[x+n].flags&cellPad != 0 {
		splitWide(cells, x+n)
	}
	copy(cells[x:], cells[x+n:])
	b := s.blank()
	for i := cols - n; i < cols; i++ {
		cells[i] = b
	}
}

// eraseCells blanks columns [x0, x1) of row y.
func (s *Screen) eraseCells(y, x0, x1 int) {
	if x0 < 0 {
		x0 = 0
	}
	if x1 > s.cols {
		x1 = s.cols
	}
	if x0 >= x1 {
		return
	}
	ln := &s.buf.lines[y]
	cells := ln.cells
	if cells[x0].flags&cellPad != 0 {
		splitWide(cells, x0)
	}
	if cells[x1-1].flags&cellWide != 0 {
		splitWide(cells, x1-1)
	}
	fillCells(cells[x0:x1], s.blank())
	if x0 == 0 && x1 == s.cols {
		ln.used = false
	}
}

func (s *Screen) eraseLine(y int) { s.eraseCells(y, 0, s.cols) }

// fillCells sets every cell to b; the common default blank is a memclr.
func fillCells(cells []cell, b cell) {
	if b == (cell{}) {
		clear(cells)
		return
	}
	for i := range cells {
		cells[i] = b
	}
}

// ---------------------------------------------------------------------------
// Scrolling

// scrollUp moves rows [top, bottom] up by n, filling the bottom with blank
// rows. When toHistory is set and the rows leave the top of the main screen
// (top == 0, as in xterm), they are saved into the history.
func (s *Screen) scrollUp(top, bottom, n int, toHistory bool) {
	if n > bottom-top+1 {
		n = bottom - top + 1
	}
	if n <= 0 {
		return
	}
	lines := s.buf.lines
	if toHistory && top == 0 && !s.altActive && s.hist.limit > 0 {
		for i := 0; i < n; i++ {
			s.pushHistory(lines[i].cells)
		}
		s.hscrolled += n
	}
	moved := append(s.scratch[:0], lines[top:top+n]...)
	copy(lines[top:], lines[top+n:bottom+1])
	b := s.blank()
	for i, l := range moved {
		fillCells(l.cells, b)
		l.used = false
		lines[bottom-n+1+i] = l
	}
	clear(moved)
	s.scratch = moved[:0]
}

// scrollDown moves rows [top, bottom] down by n, filling the top with blanks.
func (s *Screen) scrollDown(top, bottom, n int) {
	if n > bottom-top+1 {
		n = bottom - top + 1
	}
	if n <= 0 {
		return
	}
	lines := s.buf.lines
	moved := append(s.scratch[:0], lines[bottom-n+1:bottom+1]...)
	copy(lines[top+n:bottom+1], lines[top:bottom-n+1])
	b := s.blank()
	for i, l := range moved {
		fillCells(l.cells, b)
		l.used = false
		lines[top+i] = l
	}
	clear(moved)
	s.scratch = moved[:0]
}

// index moves the cursor down one row, scrolling the region when the cursor
// is on its bottom margin (LF, VT, FF, IND, and the wrap of a long line).
func (s *Screen) index() {
	switch {
	case s.cy == s.bottom:
		s.scrollUp(s.top, s.bottom, 1, true)
	case s.cy < s.rows-1:
		s.cy++
	}
}

// reverseIndex moves the cursor up one row, scrolling the region down when
// the cursor is on its top margin (RI).
func (s *Screen) reverseIndex() {
	switch {
	case s.cy == s.top:
		s.scrollDown(s.top, s.bottom, 1)
	case s.cy > 0:
		s.cy--
	}
}

// eraseDisplay implements ED. On the main screen with history enabled, a
// clear of the whole screen (ED 2, or ED 0 from the home position) first
// pushes the rows in use into the history, as tmux's default scroll-on-clear
// does, so `clear` does not destroy what an agent may still want to read.
func (s *Screen) eraseDisplay(mode int) {
	switch mode {
	case 0:
		if s.cx == 0 && s.cy == 0 && s.canScrollOnClear() {
			s.clearToHistory()
			break
		}
		s.eraseCells(s.cy, s.cx, s.cols)
		for y := s.cy + 1; y < s.rows; y++ {
			s.eraseLine(y)
		}
	case 1:
		for y := 0; y < s.cy; y++ {
			s.eraseLine(y)
		}
		s.eraseCells(s.cy, 0, s.cx+1)
	case 2:
		if s.canScrollOnClear() {
			s.clearToHistory()
			break
		}
		for y := 0; y < s.rows; y++ {
			s.eraseLine(y)
		}
	case 3:
		s.hist.clear()
		s.hscrolled = 0
		return
	default:
		return
	}
	s.pendingWrap = false
}

func (s *Screen) canScrollOnClear() bool { return !s.altActive && s.hist.limit > 0 }

// clearToHistory pushes every row up to the last used one into the history
// and blanks the screen (tmux grid_view_clear_history).
func (s *Screen) clearToHistory() {
	last := 0
	for y := range s.buf.lines {
		if s.buf.lines[y].used {
			last = y + 1
		}
	}
	for y := 0; y < last; y++ {
		s.pushHistory(s.buf.lines[y].cells)
	}
	for y := 0; y < s.rows; y++ {
		s.eraseLine(y)
	}
	s.hscrolled = 0
}

// ---------------------------------------------------------------------------
// Cursor movement

// moveTo places the cursor at an absolute position, clamped to the screen,
// and cancels any pending wrap.
func (s *Screen) moveTo(x, y int) {
	s.cx = clampInt(x, 0, s.cols-1)
	s.cy = clampInt(y, 0, s.rows-1)
	s.pendingWrap = false
}

// moveToOrigin implements CUP/HVP/VPA addressing: with origin mode set, rows
// are relative to the scroll region and confined to it.
func (s *Screen) moveToOrigin(x, y int) {
	if s.modes.OriginMode {
		s.moveTo(x, clampInt(y+s.top, s.top, s.bottom))
		return
	}
	s.moveTo(x, y)
}

// cursorUp moves up n rows, stopping at the top margin when starting inside
// the scroll region.
func (s *Screen) cursorUp(n int) {
	top := 0
	if s.cy >= s.top {
		top = s.top
	}
	s.moveTo(s.cx, max(s.cy-n, top))
}

// cursorDown moves down n rows, stopping at the bottom margin when starting
// inside the scroll region.
func (s *Screen) cursorDown(n int) {
	bottom := s.rows - 1
	if s.cy <= s.bottom {
		bottom = s.bottom
	}
	s.moveTo(s.cx, min(s.cy+n, bottom))
}

func (s *Screen) carriageReturn() {
	s.cx = 0
	s.pendingWrap = false
}

func (s *Screen) backspace() {
	if s.cx > 0 {
		s.cx--
	}
	s.pendingWrap = false
}

// ---------------------------------------------------------------------------
// Tab stops

func (s *Screen) resetTabs() {
	s.tabs = make([]bool, s.cols)
	for i := 8; i < s.cols; i += 8 {
		s.tabs[i] = true
	}
}

// tabForward moves to the n-th next tab stop, or the last column. Like tmux
// (and xterm, whose TabNext cannot move past the margin), a tab while a wrap
// is pending does nothing.
func (s *Screen) tabForward(n int) {
	if s.pendingWrap {
		return
	}
	x := s.cx
	for ; n > 0 && x < s.cols-1; n-- {
		x++
		for x < s.cols-1 && !s.tabs[x] {
			x++
		}
	}
	s.cx = x
}

// tabBackward moves to the n-th previous tab stop, or column 0 (CBT).
func (s *Screen) tabBackward(n int) {
	x := s.cx
	for ; n > 0 && x > 0; n-- {
		x--
		for x > 0 && !s.tabs[x] {
			x--
		}
	}
	s.cx = x
	s.pendingWrap = false
}

// ---------------------------------------------------------------------------
// Save/restore, buffers, resets

func (s *Screen) slot() *savedCursor {
	if s.altActive {
		return &s.saved[1]
	}
	return &s.saved[0]
}

// saveCursor implements DECSC: position, pending-wrap flag, pen, origin mode
// and character set state.
func (s *Screen) saveCursor() {
	*s.slot() = savedCursor{
		valid: true, x: s.cx, y: s.cy, pendingWrap: s.pendingWrap,
		pen: s.pen, origin: s.modes.OriginMode, g: s.g, gl: s.gl,
	}
}

// restoreCursor implements DECRC. Without a prior save xterm homes the cursor
// and resets the pen, origin mode and character sets.
func (s *Screen) restoreCursor() {
	sc := *s.slot()
	if !sc.valid {
		sc = savedCursor{g: [4]byte{'B', 'B', 'B', 'B'}}
	}
	s.cx = clampInt(sc.x, 0, s.cols-1)
	s.cy = clampInt(sc.y, 0, s.rows-1)
	s.pendingWrap = sc.pendingWrap && s.modes.Autowrap && s.cx == s.cols-1
	s.pen = sc.pen
	s.modes.OriginMode = sc.origin
	s.g = sc.g
	s.gl = sc.gl
}

// switchBuffer activates the alternate (alt=true) or main buffer and reports
// whether the active buffer changed. Contents are not cleared here.
func (s *Screen) switchBuffer(alt bool) bool {
	if s.altActive == alt {
		return false
	}
	s.altActive = alt
	if alt {
		s.buf = s.alt
	} else {
		s.buf = s.main
	}
	return true
}

func (s *Screen) clearBuffer(b *buffer) {
	bl := s.blank()
	for i := range b.lines {
		ln := &b.lines[i]
		for x := range ln.cells {
			ln.cells[x] = bl
		}
		ln.used = false
	}
}

// alignmentTest implements DECALN (ESC # 8): fill the screen with 'E', reset
// the margins and origin mode, and home the cursor.
func (s *Screen) alignmentTest() {
	for i := range s.buf.lines {
		ln := &s.buf.lines[i]
		for x := range ln.cells {
			ln.cells[x] = cell{r: 'E'}
		}
		ln.used = true
	}
	s.top, s.bottom = 0, s.rows-1
	s.modes.OriginMode = false
	s.moveTo(0, 0)
}

// fullReset implements RIS (ESC c): back to the main screen, which is cleared
// (into the history, as tmux's reset does via its clear-screen path), with
// every mode in its power-on state. The title and history are kept.
func (s *Screen) fullReset() {
	s.switchBuffer(false)
	s.clearBuffer(s.alt)
	s.pen = attr{}
	if s.canScrollOnClear() {
		s.clearToHistory()
	} else {
		s.clearBuffer(s.main)
	}
	s.resetState()
}

// softReset implements DECSTR (CSI ! p) the way xterm does: cursor visible,
// replace mode, absolute origin, normal cursor keys and keypad, full scroll
// region, default character sets and pen, and a forgotten saved cursor. The
// screen contents and the cursor position are kept. Autowrap is left on, as
// in xterm and VTE.
func (s *Screen) softReset() {
	s.modes.CursorVisible = true
	s.modes.Insert = false
	s.modes.OriginMode = false
	s.modes.AppCursor = false
	s.modes.AppKeypad = false
	s.modes.Autowrap = true
	s.top, s.bottom = 0, s.rows-1
	s.g = [4]byte{'B', 'B', 'B', 'B'}
	s.gl = 0
	s.pen = attr{}
	s.saved = [2]savedCursor{}
	s.pendingWrap = false
}

// ---------------------------------------------------------------------------
// Resize

// Resize changes the screen size without reflowing text. Rows are truncated
// or padded on the right (a wide glyph cut in half is cleared). When the
// screen gets shorter, rows below the cursor are dropped first; if that is
// not enough, rows scroll off the top into the main screen's history so the
// cursor row stays visible. When it gets taller, lines that scrolled into the
// history are pulled back first (as tmux does), then blank rows are added at
// the bottom. The scroll region resets to the full screen, the cursor is
// clamped and tab stops are kept for existing columns and set every eight
// columns in new ones.
func (s *Screen) Resize(cols, rows int) {
	cols, rows = clampSize(cols, rows)
	if cols == s.cols && rows == s.rows {
		return
	}
	if cols != s.cols {
		resizeCols(s.main, cols)
		resizeCols(s.alt, cols)
		tabs := make([]bool, cols)
		copy(tabs, s.tabs)
		for i := s.cols; i < cols; i++ {
			tabs[i] = i%8 == 0 && i > 0
		}
		s.tabs = tabs
		s.cols = cols
		s.pendingWrap = false
	}
	if rows != s.rows {
		// The active buffer pivots on the live cursor. The inactive main
		// buffer pivots on the cursor ?1049 saved for it, which is where the
		// cursor will return.
		if s.altActive {
			s.resizeRows(s.alt, rows, &s.cy, false)
			y := s.cy
			if s.saved[0].valid {
				y = s.saved[0].y
			}
			y = clampInt(y, 0, len(s.main.lines)-1)
			s.resizeRows(s.main, rows, &y, true)
			if s.saved[0].valid {
				s.saved[0].y = y
			}
		} else {
			s.resizeRows(s.main, rows, &s.cy, true)
			y := 0
			s.resizeRows(s.alt, rows, &y, false)
		}
		s.rows = rows
	}
	s.top, s.bottom = 0, s.rows-1
	s.cx = clampInt(s.cx, 0, s.cols-1)
	s.cy = clampInt(s.cy, 0, s.rows-1)
	for i := range s.saved {
		s.saved[i].x = clampInt(s.saved[i].x, 0, s.cols-1)
		s.saved[i].y = clampInt(s.saved[i].y, 0, s.rows-1)
	}
}

func resizeCols(b *buffer, cols int) {
	for i := range b.lines {
		b.lines[i].cells = fitCells(b.lines[i].cells, cols)
	}
}

// fitCells returns cells truncated or padded with blanks to exactly cols,
// clearing a wide glyph whose right half would be cut off.
func fitCells(cells []cell, cols int) []cell {
	out := make([]cell, cols)
	copy(out, cells)
	if len(cells) > cols && out[cols-1].flags&cellWide != 0 {
		out[cols-1] = cell{}
	}
	return out
}

func (s *Screen) resizeRows(b *buffer, rows int, cy *int, isMain bool) {
	old := len(b.lines)
	switch {
	case rows < old:
		needed := old - rows
		avail := min(max(old-1-*cy, 0), needed)
		lines := b.lines[:old-avail]
		needed -= avail
		if needed > 0 {
			if isMain && s.hist.limit > 0 {
				for i := 0; i < needed; i++ {
					s.pushHistory(lines[i].cells)
				}
				s.hscrolled += needed
			}
			lines = lines[needed:]
			*cy -= needed
		}
		b.lines = append([]line(nil), lines...)
	case rows > old:
		needed := rows - old
		var pulled []line
		if isMain {
			n := min(needed, s.hscrolled, s.hist.len())
			pulled = make([]line, n)
			for i := n - 1; i >= 0; i-- {
				text := s.hist.popNewest()
				pulled[i] = line{cells: s.cellsFromText(text), used: text != ""}
			}
			s.hscrolled -= n
			*cy += n
			needed -= n
		}
		lines := make([]line, 0, rows)
		lines = append(lines, pulled...)
		lines = append(lines, b.lines...)
		for i := 0; i < needed; i++ {
			lines = append(lines, line{cells: make([]cell, s.cols)})
		}
		b.lines = lines
	}
}

// ---------------------------------------------------------------------------
// History

// history is a bounded FIFO of the text of scrolled-off rows (see
// pushHistory). Once full it is a ring that overwrites the oldest line.
type history struct {
	lines []string
	head  int // index of the oldest line when the ring is full
	limit int
}

func (h *history) len() int { return len(h.lines) }

// at returns the i-th oldest line.
func (h *history) at(i int) string {
	return h.lines[(h.head+i)%len(h.lines)]
}

func (h *history) push(l string) {
	if h.limit <= 0 {
		return
	}
	if len(h.lines) < h.limit {
		h.lines = append(h.lines, l)
		return
	}
	h.lines[h.head] = l
	h.head = (h.head + 1) % len(h.lines)
}

// popNewest removes and returns the most recent line.
func (h *history) popNewest() string {
	if h.head != 0 {
		// Linearize the ring so the newest line is at the end.
		lin := make([]string, 0, len(h.lines))
		lin = append(lin, h.lines[h.head:]...)
		lin = append(lin, h.lines[:h.head]...)
		h.lines, h.head = lin, 0
	}
	n := len(h.lines) - 1
	l := h.lines[n]
	h.lines = h.lines[:n]
	return l
}

func (h *history) clear() {
	h.lines = nil
	h.head = 0
}
