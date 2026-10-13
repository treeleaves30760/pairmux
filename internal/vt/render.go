package vt

import (
	"strconv"
	"unicode/utf8"
)

// Render returns bytes that repaint the active buffer on an xterm-compatible
// terminal of the same size that is already on its alternate screen, and put
// that terminal into the program's modes. The output never switches screen
// buffers.
//
// It hides the cursor, resets the scroll region, origin mode, insert mode,
// character sets and SGR, clears the screen, and draws each row from its
// first column with SGR changes only where attributes change. Autowrap is on
// while drawing so the bottom-right cell leaves the cursor pending a wrap
// instead of scrolling. It then restores the scroll region, origin mode, the
// cursor position (re-drawing the last glyph of the row when a wrap is
// pending, which is the only way to recreate that state), insert mode,
// autowrap, cursor key and keypad modes, bracketed paste, focus events, mouse
// tracking and encoding (turned off explicitly when off, to correct a stale
// terminal), cursor style, character sets, the pen, the title (if set) and
// finally cursor visibility.
func (s *Screen) Render() []byte {
	return s.appendRender(make([]byte, 0, s.rows*(s.cols+16)+256))
}

func (s *Screen) appendRender(b []byte) []byte {
	b = append(b, "\x1b[?25l\x1b[?6l\x1b[r\x1b[4l\x1b[?7h"...)
	b = append(b, "\x1b(B\x1b)B\x1b*B\x1b+B\x0f"...)
	b = append(b, "\x1b[0m\x1b[H\x1b[2J"...)

	var cur attr
	for y := range s.buf.lines {
		cells := s.buf.lines[y].cells
		last := len(cells) - 1
		for last >= 0 && cells[last] == (cell{}) {
			last--
		}
		if last < 0 {
			continue
		}
		b = appendCUP(b, y+1, 1)
		for x := 0; x <= last; x++ {
			c := &cells[x]
			if c.flags&cellPad != 0 {
				continue
			}
			if c.a != cur {
				b = appendSGR(b, cur, c.a)
				cur = c.a
			}
			b = appendCellText(b, c)
		}
	}

	if s.top != 0 || s.bottom != s.rows-1 {
		b = append(b, "\x1b["...)
		b = strconv.AppendInt(b, int64(s.top+1), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(s.bottom+1), 10)
		b = append(b, 'r')
	}
	rowBase := 0
	if s.modes.OriginMode {
		b = append(b, "\x1b[?6h"...)
		rowBase = s.top
	}
	row := max(s.cy-rowBase, 0)
	if s.pendingWrap {
		cells := s.buf.lines[s.cy].cells
		x := s.cols - 1
		if cells[x].flags&cellPad != 0 && x > 0 {
			x--
		}
		c := &cells[x]
		b = appendCUP(b, row+1, x+1)
		if c.a != cur {
			b = appendSGR(b, cur, c.a)
			cur = c.a
		}
		b = appendCellText(b, c)
	} else {
		b = appendCUP(b, row+1, s.cx+1)
	}

	m := &s.modes
	if m.Insert {
		b = append(b, "\x1b[4h"...)
	}
	if !m.Autowrap {
		b = append(b, "\x1b[?7l"...)
	}
	b = appendMode(b, 1, m.AppCursor)
	if m.AppKeypad {
		b = append(b, "\x1b="...)
	} else {
		b = append(b, "\x1b>"...)
	}
	b = appendMode(b, 2004, m.BracketedPaste)
	b = appendMode(b, 1004, m.FocusEvents)
	b = append(b, "\x1b[?9;1000;1002;1003l"...)
	if m.MouseTracking != 0 {
		b = appendMode(b, m.MouseTracking, true)
	}
	b = append(b, "\x1b[?1005;1006;1015;1016l"...)
	if m.MouseEncoding != 0 {
		b = appendMode(b, m.MouseEncoding, true)
	}
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(s.cursorStyle), 10)
	b = append(b, " q"...)

	for i, set := range s.g {
		if set == '0' {
			b = append(b, esc, "()*+"[i], '0')
		}
	}
	switch s.gl {
	case 1:
		b = append(b, 0x0e)
	case 2:
		b = append(b, esc, 'n')
	case 3:
		b = append(b, esc, 'o')
	}

	b = appendSGR(b, cur, s.pen)
	if s.title != "" {
		b = append(b, "\x1b]2;"...)
		b = append(b, s.title...)
		b = append(b, bel)
	}
	if m.CursorVisible {
		b = append(b, "\x1b[?25h"...)
	}
	return b
}

func appendCellText(b []byte, c *cell) []byte {
	if c.r == 0 {
		return append(b, ' ')
	}
	b = utf8.AppendRune(b, c.r)
	return append(b, c.comb...)
}

func appendCUP(b []byte, row, col int) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(row), 10)
	if col != 1 {
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(col), 10)
	}
	return append(b, 'H')
}

func appendMode(b []byte, mode int, set bool) []byte {
	b = append(b, "\x1b[?"...)
	b = strconv.AppendInt(b, int64(mode), 10)
	if set {
		return append(b, 'h')
	}
	return append(b, 'l')
}

// sgrFlags maps attribute flags to the SGR parameter that sets them.
var sgrFlags = [...]struct {
	flag uint16
	code string
}{
	{attrBold, "1"}, {attrDim, "2"}, {attrItalic, "3"}, {attrBlink, "5"},
	{attrReverse, "7"}, {attrInvisible, "8"}, {attrStrike, "9"}, {attrOverline, "53"},
}

// appendSGR appends an SGR sequence changing the rendition from `from` to
// `to`. Additions are emitted alone; anything that must be switched off
// starts from a reset ("0"), which is shorter and more portable than the
// individual "off" codes.
func appendSGR(b []byte, from, to attr) []byte {
	if from == to {
		return b
	}
	b = append(b, "\x1b["...)
	start := len(b)
	if to == (attr{}) || from.flags&^to.flags != 0 ||
		(from.ul != 0 && to.ul == 0) ||
		(from.fg != 0 && to.fg == 0) || (from.bg != 0 && to.bg == 0) ||
		(from.ulc != 0 && to.ulc == 0) {
		b = append(b, '0')
		from = attr{}
	}
	sep := func() {
		if len(b) > start {
			b = append(b, ';')
		}
	}
	for _, f := range sgrFlags {
		if to.flags&f.flag != 0 && from.flags&f.flag == 0 {
			sep()
			b = append(b, f.code...)
		}
	}
	if to.ul != from.ul {
		sep()
		if to.ul == 1 {
			b = append(b, '4')
		} else {
			b = append(b, "4:"...)
			b = strconv.AppendInt(b, int64(to.ul), 10)
		}
	}
	if to.fg != from.fg {
		sep()
		b = appendColor(b, to.fg, 30, 90, "38")
	}
	if to.bg != from.bg {
		sep()
		b = appendColor(b, to.bg, 40, 100, "48")
	}
	if to.ulc != from.ulc {
		sep()
		// Colon form: terminals that do not know 58 then skip the whole
		// parameter instead of misreading the components as other SGRs.
		switch to.ulc & colorKind {
		case colorIndexed:
			b = append(b, "58:5:"...)
			b = strconv.AppendInt(b, int64(to.ulc&0xff), 10)
		case colorRGB:
			b = append(b, "58:2::"...)
			b = appendRGB(b, to.ulc, ':')
		}
	}
	return append(b, 'm')
}

// appendColor appends a foreground or background color parameter: the
// classic 8-color and bright codes for palette entries 0–15, the 256-color
// form for other palette entries and the direct form for RGB.
func appendColor(b []byte, c color, base, brightBase int, ext string) []byte {
	switch c & colorKind {
	case colorIndexed:
		i := int(c & 0xff)
		switch {
		case i < 8:
			return strconv.AppendInt(b, int64(base+i), 10)
		case i < 16:
			return strconv.AppendInt(b, int64(brightBase+i-8), 10)
		}
		b = append(b, ext...)
		b = append(b, ";5;"...)
		return strconv.AppendInt(b, int64(i), 10)
	case colorRGB:
		b = append(b, ext...)
		b = append(b, ";2;"...)
		return appendRGB(b, c, ';')
	}
	return strconv.AppendInt(b, int64(base+9), 10) // 39 / 49: default
}

func appendRGB(b []byte, c color, sep byte) []byte {
	b = strconv.AppendInt(b, int64(c>>16&0xff), 10)
	b = append(b, sep)
	b = strconv.AppendInt(b, int64(c>>8&0xff), 10)
	b = append(b, sep)
	return strconv.AppendInt(b, int64(c&0xff), 10)
}
