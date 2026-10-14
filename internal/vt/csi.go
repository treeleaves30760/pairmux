package vt

import "strconv"

// maxParams bounds how many CSI parameters (including ':' sub-parameters)
// are kept; extra ones are ignored, as in xterm.
const maxParams = 32

// maxParamValue saturates parameter values so arithmetic on them cannot
// overflow and loops driven by them (REP, IL, ...) stay bounded.
const maxParamValue = 65535

// params holds the parsed parameters of one CSI sequence. Values are -1 when
// omitted. sub[i] marks v[i] as a ':' sub-parameter of the preceding
// parameter (ITU T.416 style, e.g. 38:2::r:g:b or 4:3).
type params struct {
	n   int
	v   [maxParams]int32
	sub [maxParams]bool
	// top-level (non-sub) parameter positions, for sequences that do not use
	// sub-parameters.
	tn  int
	top [maxParams]uint8
}

// parse fills p from the parameter bytes (digits, ';' and ':'). It reports
// false for bytes that cannot appear there, such as a private marker in the
// middle.
func (p *params) parse(b []byte) bool {
	*p = params{}
	if len(b) == 0 {
		return true
	}
	cur, have, isSub := int32(0), false, false
	flush := func() {
		if p.n < maxParams {
			if have {
				p.v[p.n] = cur
			} else {
				p.v[p.n] = -1
			}
			p.sub[p.n] = isSub
			if !isSub {
				p.top[p.tn] = uint8(p.n)
				p.tn++
			}
			p.n++
		}
		cur, have = 0, false
	}
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9':
			if cur < maxParamValue {
				cur = cur*10 + int32(c-'0')
				if cur > maxParamValue {
					cur = maxParamValue
				}
			}
			have = true
		case c == ';':
			flush()
			isSub = false
		case c == ':':
			flush()
			isSub = true
		case c == 0x7f:
			// DEL is ignored anywhere in a sequence.
		default:
			return false
		}
	}
	flush()
	return true
}

// arg returns top-level parameter i, or def when it is absent or omitted.
func (p *params) arg(i, def int) int {
	if i >= p.tn {
		return def
	}
	v := p.v[p.top[i]]
	if v < 0 {
		return def
	}
	return int(v)
}

// count returns top-level parameter i treating omitted and 0 as 1, the
// convention for movement and repetition counts.
func (p *params) count(i int) int {
	v := p.arg(i, 1)
	if v == 0 {
		return 1
	}
	return v
}

const (
	da1Reply = "\x1b[?1;2c"    // VT100 with Advanced Video Option
	da2Reply = "\x1b[>0;10;1c" // VT100-class, firmware 10, no ROM cartridge
	xtVerRep = "\x1bP>|pairmux\x1b\\"
)

// dispatchCSI executes the complete CSI sequence in s.seq and reports whether
// it should be forwarded unchanged. Sequences it does not understand are
// forwarded and otherwise ignored.
func (s *Screen) dispatchCSI() bool {
	body := s.seq[2:]
	final := body[len(body)-1]
	body = body[:len(body)-1]
	var priv byte
	if len(body) > 0 && body[0] >= '<' && body[0] <= '?' {
		priv, body = body[0], body[1:]
	}
	k := 0
	for k < len(body) && (body[k] >= 0x30 && body[k] <= 0x3f || body[k] == 0x7f) {
		k++
	}
	pbytes, inter := body[:k], body[k:]
	for _, c := range inter {
		if (c < 0x20 || c > 0x2f) && c != 0x7f {
			return true // parameter bytes after intermediates: malformed
		}
	}
	var p params
	if !p.parse(pbytes) {
		return true
	}
	if len(inter) > 1 {
		return true
	}
	var im byte
	if len(inter) == 1 {
		im = inter[0]
	}
	switch {
	case im == 0 && priv == 0:
		return s.csiPlain(final, &p)
	case im == 0 && priv == '?':
		return s.csiPrivate(final, &p)
	case im == 0:
		return s.csiOtherPrivate(priv, final, &p)
	default:
		return s.csiIntermediate(priv, im, final, &p)
	}
}

// csiPlain handles CSI sequences without private marker or intermediate.
func (s *Screen) csiPlain(final byte, p *params) bool {
	switch final {
	case 'A': // CUU
		s.cursorUp(p.count(0))
	case 'B': // CUD
		s.cursorDown(p.count(0))
	case 'e': // VPR
		s.cursorDown(p.count(0))
	case 'C', 'a': // CUF, HPR
		s.moveTo(s.cx+p.count(0), s.cy)
	case 'D': // CUB
		s.moveTo(s.cx-p.count(0), s.cy)
	case 'E': // CNL
		s.cursorDown(p.count(0))
		s.cx = 0
	case 'F': // CPL
		s.cursorUp(p.count(0))
		s.cx = 0
	case 'G', '`': // CHA, HPA
		s.moveTo(p.count(0)-1, s.cy)
	case 'H', 'f': // CUP, HVP
		s.moveToOrigin(p.count(1)-1, p.count(0)-1)
	case 'd': // VPA
		y := p.count(0) - 1
		if s.modes.OriginMode {
			s.moveToOrigin(s.cx, y)
		} else {
			s.moveTo(s.cx, y)
		}
	case 'I': // CHT
		s.tabForward(p.count(0))
	case 'Z': // CBT
		s.tabBackward(p.count(0))
	case 'J': // ED
		s.eraseDisplay(p.arg(0, 0))
	case 'K': // EL
		s.eraseInLine(p.arg(0, 0))
	case '@': // ICH
		s.pendingWrap = false
		s.insertCells(s.buf.lines[s.cy].cells, s.cx, p.count(0))
	case 'P': // DCH
		s.pendingWrap = false
		s.deleteCells(s.buf.lines[s.cy].cells, s.cx, p.count(0))
	case 'X': // ECH
		s.pendingWrap = false
		s.eraseCells(s.cy, s.cx, s.cx+p.count(0))
	case 'L': // IL
		s.insertLines(p.count(0))
	case 'M': // DL
		s.deleteLines(p.count(0))
	case 'S': // SU
		s.scrollUp(s.top, s.bottom, p.count(0), true)
	case 'T': // SD; with several parameters it is xterm's mouse highlight tracking
		if p.tn <= 1 {
			s.scrollDown(s.top, s.bottom, p.count(0))
		}
	case 'b': // REP
		if s.lastRune != 0 {
			r, w := s.lastRune, s.lastWidth
			for n := p.count(0); n > 0; n-- {
				s.printGlyph(r, w)
			}
		}
	case 'r': // DECSTBM
		top := p.count(0) - 1
		bottom := p.arg(1, 0) - 1
		if bottom < 0 || bottom >= s.rows {
			bottom = s.rows - 1
		}
		if top < bottom {
			s.top, s.bottom = top, bottom
			s.moveToOrigin(0, 0)
		}
	case 's': // SCOSC (DECSLRM when left/right margins exist; we have none)
		if p.n == 0 {
			s.saveCursor()
		}
	case 'u': // SCORC
		if p.n == 0 {
			s.restoreCursor()
		}
	case 'g': // TBC
		switch p.arg(0, 0) {
		case 0:
			s.tabs[s.cx] = false
		case 3:
			clear(s.tabs)
		}
	case 'm': // SGR
		s.sgr(p)
	case 'h', 'l': // SM/RM
		for i := 0; i < p.tn; i++ {
			if p.arg(i, -1) == 4 {
				s.modes.Insert = final == 'h'
			}
		}
	case 'n': // DSR
		switch p.arg(0, 0) {
		case 5:
			s.reply = append(s.reply, "\x1b[0n"...)
		case 6:
			s.reply = s.appendCPR(s.reply, false)
		}
		return false
	case 'c': // DA1
		if p.arg(0, 0) == 0 {
			s.reply = append(s.reply, da1Reply...)
		}
		return false
	case 't':
		return s.windowOp(p)
	case 'x': // DECREQTPARM: an obsolete query
		return false
	}
	return true
}

// csiPrivate handles "CSI ? ..." sequences.
func (s *Screen) csiPrivate(final byte, p *params) bool {
	switch final {
	case 'h', 'l':
		return s.decset(p, final == 'h')
	case 'J': // DECSED: selective erase; nothing is protected, so it is ED
		s.eraseDisplay(p.arg(0, 0))
	case 'K': // DECSEL
		s.eraseInLine(p.arg(0, 0))
	case 'n': // DECDSR: only the extended cursor position report is answered
		if p.arg(0, 0) == 6 {
			s.reply = s.appendCPR(s.reply, true)
		}
		return false
	case 'u': // kitty keyboard protocol query
		return false
	case 'S': // XTSMGRAPHICS query/set
		return false
	case 'm': // XTQMODKEYS query
		return false
	case 'W': // DECST8C: reset tab stops to every 8 columns
		if p.arg(0, 0) == 5 {
			s.resetTabs()
		}
	case 'c': // a DA1 response, never a request
		return false
	}
	return true
}

// csiOtherPrivate handles "CSI > ...", "CSI < ..." and "CSI = ...".
func (s *Screen) csiOtherPrivate(priv, final byte, p *params) bool {
	switch final {
	case 'c':
		if priv == '>' && p.arg(0, 0) == 0 {
			s.reply = append(s.reply, da2Reply...)
		}
		return false // DA2 / DA3 (no reply: we have no unit ID)
	case 'q':
		if priv == '>' && p.arg(0, 0) == 0 {
			s.reply = append(s.reply, xtVerRep...)
			return false
		}
	case 'u':
		// Kitty keyboard protocol push (>), pop (<) and set (=). Removing
		// them keeps the human's terminal sending legacy key encodings, which
		// is what the program falls back to without a query answer.
		return false
	}
	return true
}

// csiIntermediate handles sequences with one intermediate byte.
func (s *Screen) csiIntermediate(priv, im, final byte, p *params) bool {
	switch {
	case im == ' ' && final == 'q' && priv == 0: // DECSCUSR
		if v := p.arg(0, 0); v <= 6 {
			s.cursorStyle = v
		}
	case im == '$' && final == 'p' && (priv == 0 || priv == '?'): // DECRQM
		s.reportMode(priv == '?', p.arg(0, 0))
		return false
	case im == '!' && final == 'p' && priv == 0: // DECSTR
		s.softReset()
	}
	return true
}

// eraseInLine implements EL. Like xterm, erasing cancels a pending wrap, so
// "EL 0" after a glyph in the last column erases that glyph.
func (s *Screen) eraseInLine(mode int) {
	switch mode {
	case 0:
		s.eraseCells(s.cy, s.cx, s.cols)
	case 1:
		s.eraseCells(s.cy, 0, s.cx+1)
	case 2:
		s.eraseCells(s.cy, 0, s.cols)
	default:
		return
	}
	s.pendingWrap = false
}

// insertLines implements IL. Inside the scroll region it shifts the rows from
// the cursor to the bottom margin down; outside it, like tmux, it shifts the
// rows from the cursor to the bottom of the screen. The cursor column is kept.
func (s *Screen) insertLines(n int) {
	bottom := s.bottom
	if s.cy < s.top || s.cy > s.bottom {
		bottom = s.rows - 1
	}
	s.scrollDown(s.cy, bottom, n)
	s.pendingWrap = false
}

// deleteLines implements DL (see insertLines). Deleted rows never enter the
// history.
func (s *Screen) deleteLines(n int) {
	bottom := s.bottom
	if s.cy < s.top || s.cy > s.bottom {
		bottom = s.rows - 1
	}
	s.scrollUp(s.cy, bottom, n, false)
	s.pendingWrap = false
}

// appendCPR appends a cursor position report (1-based; relative to the
// scroll region when origin mode is set).
func (s *Screen) appendCPR(b []byte, private bool) []byte {
	row := s.cy + 1
	if s.modes.OriginMode {
		row -= s.top
	}
	b = append(b, "\x1b["...)
	if private {
		b = append(b, '?')
	}
	b = strconv.AppendInt(b, int64(row), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(s.cx+1), 10)
	return append(b, 'R')
}

// windowOp handles XTWINOPS (CSI Ps t). Size reports are answered from the
// virtual screen (assuming an 8x16 pixel cell); operations that would move,
// resize, raise or iconify the human's real window are removed; the title
// stack operations (22/23) pass through.
func (s *Screen) windowOp(p *params) bool {
	r := s.reply
	switch p.arg(0, 0) {
	case 11: // report window state: not iconified
		r = append(r, "\x1b[1t"...)
	case 13: // report window position
		r = append(r, "\x1b[3;0;0t"...)
	case 14: // report text area size in pixels
		r = appendReport(r, 4, s.rows*16, s.cols*8)
	case 15: // report screen size in pixels
		r = appendReport(r, 5, s.rows*16, s.cols*8)
	case 16: // report cell size in pixels
		r = append(r, "\x1b[6;16;8t"...)
	case 18: // report text area size in characters
		r = appendReport(r, 8, s.rows, s.cols)
	case 19: // report screen size in characters
		r = appendReport(r, 9, s.rows, s.cols)
	case 22, 23: // push/pop title
		return true
	}
	s.reply = r
	return false
}

func appendReport(b []byte, code, a, c int) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(code), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(a), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(c), 10)
	return append(b, 't')
}

// reportMode answers DECRQM with 1 (set), 2 (reset) or 0 (not recognized).
func (s *Screen) reportMode(private bool, mode int) {
	pm := 0
	set := func(v bool) {
		if v {
			pm = 1
		} else {
			pm = 2
		}
	}
	m := &s.modes
	if private {
		switch mode {
		case 1:
			set(m.AppCursor)
		case 6:
			set(m.OriginMode)
		case 7:
			set(m.Autowrap)
		case 9, 1000, 1002, 1003:
			set(m.MouseTracking == mode)
		case 25:
			set(m.CursorVisible)
		case 47, 1047, 1049:
			set(s.altActive)
		case 66:
			set(m.AppKeypad)
		case 1004:
			set(m.FocusEvents)
		case 1005, 1006, 1015, 1016:
			set(m.MouseEncoding == mode)
		case 2004:
			set(m.BracketedPaste)
		}
	} else if mode == 4 {
		set(m.Insert)
	}
	b := append(s.reply, "\x1b["...)
	if private {
		b = append(b, '?')
	}
	b = strconv.AppendInt(b, int64(mode), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(pm), 10)
	s.reply = append(b, "$y"...)
}

// isFilteredDECMode reports DEC private modes that must not reach the human's
// terminal: alternate screen switches (emulated, with a repaint instead),
// DECCOLM (would resize the real terminal and clear it) and Win32 input mode
// (ConPTY asks for it; a real terminal would start sending Win32 key records).
func isFilteredDECMode(v int) bool {
	switch v {
	case 3, 47, 1047, 1049, 9001:
		return true
	}
	return false
}

// decset handles DECSET/DECRST (CSI ? Pm h / l). When the sequence mixes
// filtered modes with others, the others are re-emitted as their own
// sequences at their original position relative to the filtered ones.
func (s *Screen) decset(p *params, set bool) bool {
	filtered := false
	for i := 0; i < p.tn; i++ {
		if isFilteredDECMode(p.arg(i, -1)) {
			filtered = true
		}
	}
	if !filtered {
		for i := 0; i < p.tn; i++ {
			s.setDECMode(p.arg(i, -1), set)
		}
		return true
	}
	var keep [maxParams]int
	nk := 0
	flush := func() {
		if nk == 0 {
			return
		}
		s.out = append(s.out, "\x1b[?"...)
		for i := 0; i < nk; i++ {
			if i > 0 {
				s.out = append(s.out, ';')
			}
			s.out = strconv.AppendInt(s.out, int64(keep[i]), 10)
		}
		if set {
			s.out = append(s.out, 'h')
		} else {
			s.out = append(s.out, 'l')
		}
		nk = 0
	}
	for i := 0; i < p.tn; i++ {
		v := p.arg(i, -1)
		switch v {
		case -1, 3, 9001:
		case 47, 1047, 1049:
			flush()
			s.altScreen(v, set)
		default:
			s.setDECMode(v, set)
			keep[nk] = v
			nk++
		}
	}
	flush()
	return false
}

// setDECMode applies one DEC private mode that is passed through.
func (s *Screen) setDECMode(v int, set bool) {
	m := &s.modes
	switch v {
	case 1:
		m.AppCursor = set
	case 6:
		m.OriginMode = set
		s.moveToOrigin(0, 0)
	case 7:
		m.Autowrap = set
		if !set {
			s.pendingWrap = false
		}
	case 9, 1000, 1002, 1003:
		// xterm keeps one mouse mode; resetting any of them turns
		// reporting off.
		if set {
			m.MouseTracking = v
		} else {
			m.MouseTracking = 0
		}
	case 25:
		m.CursorVisible = set
	case 66:
		m.AppKeypad = set
	case 1004:
		m.FocusEvents = set
	case 1005, 1006, 1015, 1016:
		if set {
			m.MouseEncoding = v
		} else {
			m.MouseEncoding = 0
		}
	case 1048:
		if set {
			s.saveCursor()
		} else {
			s.restoreCursor()
		}
	case 2004:
		m.BracketedPaste = set
	}
}

// cursorState is what a buffer switch can change besides the buffer itself.
type cursorState struct {
	x, y        int
	pendingWrap bool
	pen         attr
	origin      bool
	g           [4]byte
	gl          int
}

func (s *Screen) cursorState() cursorState {
	return cursorState{s.cx, s.cy, s.pendingWrap, s.pen, s.modes.OriginMode, s.g, s.gl}
}

// altScreen applies an alternate screen mode with xterm's semantics and, if
// the active buffer (or, for ?1049l, the restored cursor) changed, appends a
// repaint to the forward stream in place of the removed switch:
//
//	?47h   switch to the alternate buffer          ?47l   switch to main
//	?1047h switch to the alternate buffer          ?1047l clear alternate, switch to main
//	?1049h save cursor, clear alternate, switch    ?1049l switch to main, restore cursor
func (s *Screen) altScreen(v int, set bool) {
	before := s.cursorState()
	changed := false
	switch {
	case v == 47:
		changed = s.switchBuffer(set)
	case v == 1047 && set:
		changed = s.switchBuffer(true)
	case v == 1047:
		if s.altActive {
			s.clearBuffer(s.alt)
		}
		changed = s.switchBuffer(false)
	case v == 1049 && set:
		s.saveCursor()
		if !s.altActive {
			s.clearBuffer(s.alt)
			changed = s.switchBuffer(true)
		}
	case v == 1049:
		changed = s.switchBuffer(false)
		s.restoreCursor()
		changed = changed || s.cursorState() != before
	}
	if changed {
		s.out = s.appendRender(s.out)
	}
}

// sgr applies Select Graphic Rendition parameters to the pen.
func (s *Screen) sgr(p *params) {
	if p.n == 0 {
		s.pen = attr{}
		return
	}
	a := &s.pen
	for i := 0; i < p.n; {
		v := p.v[i]
		j := i + 1
		for j < p.n && p.sub[j] {
			j++
		}
		subs := p.v[i+1 : j]
		switch {
		case v <= 0:
			*a = attr{}
		case v == 1:
			a.flags |= attrBold
		case v == 2:
			a.flags |= attrDim
		case v == 3:
			a.flags |= attrItalic
		case v == 4:
			a.ul = 1
			if len(subs) > 0 {
				switch st := subs[0]; {
				case st <= 0:
					a.ul = 0
				case st <= 5:
					a.ul = uint8(st)
				}
			}
		case v == 5 || v == 6:
			a.flags |= attrBlink
		case v == 7:
			a.flags |= attrReverse
		case v == 8:
			a.flags |= attrInvisible
		case v == 9:
			a.flags |= attrStrike
		case v == 21:
			a.ul = 2
		case v == 22:
			a.flags &^= attrBold | attrDim
		case v == 23:
			a.flags &^= attrItalic
		case v == 24:
			a.ul = 0
		case v == 25:
			a.flags &^= attrBlink
		case v == 27:
			a.flags &^= attrReverse
		case v == 28:
			a.flags &^= attrInvisible
		case v == 29:
			a.flags &^= attrStrike
		case v >= 30 && v <= 37:
			a.fg = colorIndexed | color(v-30)
		case v == 39:
			a.fg = colorDefault
		case v >= 40 && v <= 47:
			a.bg = colorIndexed | color(v-40)
		case v == 49:
			a.bg = colorDefault
		case v == 53:
			a.flags |= attrOverline
		case v == 55:
			a.flags &^= attrOverline
		case v == 59:
			a.ulc = colorDefault
		case v >= 90 && v <= 97:
			a.fg = colorIndexed | color(v-90+8)
		case v >= 100 && v <= 107:
			a.bg = colorIndexed | color(v-100+8)
		case v == 38 || v == 48 || v == 58:
			var c color
			var ok bool
			if len(subs) > 0 {
				c, ok = extColor(subs, true)
			} else {
				var used int
				c, ok, used = extColorSemi(p, j)
				if used == 0 {
					return // truncated: xterm ignores the rest of the sequence
				}
				j += used
			}
			if ok {
				switch v {
				case 38:
					a.fg = c
				case 48:
					a.bg = c
				default:
					a.ulc = c
				}
			}
		}
		i = j
	}
}

// extColor decodes the colon form of an extended color: "5:n" or
// "2:[colorspace]:r:g:b" (the colorspace field may be absent).
func extColor(subs []int32, colon bool) (color, bool) {
	if len(subs) == 0 {
		return 0, false
	}
	switch subs[0] {
	case 5:
		if len(subs) >= 2 && subs[1] >= 0 && subs[1] <= 255 {
			return colorIndexed | color(subs[1]), true
		}
	case 2:
		rgb := subs[1:]
		if colon && len(rgb) >= 4 {
			rgb = rgb[1:] // skip the colorspace id
		}
		if len(rgb) >= 3 {
			var c color
			for _, x := range rgb[:3] {
				if x < 0 {
					x = 0
				}
				if x > 255 {
					return 0, false
				}
				c = c<<8 | color(x)
			}
			return colorRGB | c, true
		}
	}
	return 0, false
}

// extColorSemi decodes the semicolon form "38;5;n" / "38;2;r;g;b" whose
// selector is parameter i (just after the 38/48/58). It reports how many
// parameters the form spans (0 when it is truncated) and whether the color
// was valid.
func extColorSemi(p *params, i int) (c color, ok bool, used int) {
	if i >= p.n {
		return 0, false, 0
	}
	switch p.v[i] {
	case 5:
		if i+1 < p.n {
			c, ok = extColor([]int32{5, p.v[i+1]}, false)
			return c, ok, 2
		}
	case 2:
		if i+3 < p.n {
			c, ok = extColor([]int32{2, p.v[i+1], p.v[i+2], p.v[i+3]}, false)
			return c, ok, 4
		}
	}
	return 0, false, 0
}
