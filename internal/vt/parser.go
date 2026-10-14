package vt

import (
	"bytes"
	"strconv"
	"unicode/utf8"
)

// pstate is the parser state, a simplified version of the DEC/ANSI state
// machine described by Paul Williams (vt100.net/emu/dec_ansi_parser), with
// the CSI parameter sub-states folded into one (parameters are parsed at
// dispatch time from the raw bytes) and DCS treated as a string that is
// parsed once complete.
type pstate uint8

const (
	stGround    pstate = iota // text and C0 controls
	stEsc                     // after ESC
	stEscInter                // ESC followed by intermediate bytes (0x20–0x2F)
	stCSI                     // inside CSI, before the final byte
	stString                  // inside an OSC/DCS/SOS/PM/APC string
	stStringEsc               // saw ESC inside a string: ST, or an abort
)

const (
	esc = 0x1b
	bel = 0x07
	can = 0x18
	sub = 0x1a

	// maxString caps how many bytes of an OSC/DCS/SOS/PM/APC string are
	// buffered for inspection. A longer string (sixel images, large OSC 52
	// clipboard writes) is flushed to the forward stream as-is once it
	// crosses the cap, and the rest passes through unfiltered as it arrives.
	maxString = 64 << 10

	// maxCSI caps a CSI sequence's buffered length. Real sequences are a few
	// dozen bytes; a longer one is passed through undispatched.
	maxCSI = 512
)

// Feed consumes program output and returns two byte streams.
//
// forward is the input with filtered sequences removed (and, at alternate
// screen switches, a repaint inserted); see the package documentation for
// exactly what is filtered. Bytes of a sequence or UTF-8 character that is
// incomplete when p ends are held and emitted by a later Feed once complete,
// so forward never ends mid-sequence.
//
// reply holds the answers to device queries found in p, in order; the caller
// writes them to the program's input. Both slices are freshly allocated and
// owned by the caller.
func (s *Screen) Feed(p []byte) (forward, reply []byte) {
	if s.discardForward {
		s.out = s.fwdScratch[:0]
	} else {
		s.out = make([]byte, 0, len(p)+len(s.seq)+s.un+16)
	}
	s.reply = nil
	i := 0
	for i < len(p) {
		switch s.state {
		case stGround:
			i = s.ground(p, i)
		case stString:
			i = s.stringBytes(p, i)
		default:
			if s.step(p[i]) {
				i++
			}
		}
	}
	if cap(s.seq) > 4096 && s.state == stGround {
		s.seq = nil // do not pin a large string buffer between sequences
	}
	forward, reply = s.out, s.reply
	s.out, s.reply = nil, nil
	if s.discardForward {
		if cap(forward) <= 1<<20 {
			s.fwdScratch = forward[:0] // reused by the next Feed; never handed out
		}
		forward = nil
	}
	return forward, reply
}

// SetForwarding turns the forward stream on or off. With it off, Feed still
// updates the screen and still returns replies, but builds its forward bytes
// in a reused scratch buffer and returns none: the multiplexer turns it off
// while no human is attached, which is most of the time for agents, and an
// attaching human starts from Render anyway.
func (s *Screen) SetForwarding(on bool) { s.discardForward = !on }

// ground processes bytes in the ground state starting at p[i] and returns the
// index of the first unprocessed byte. It returns early when an ESC moves the
// parser into another state.
func (s *Screen) ground(p []byte, i int) int {
	if s.un > 0 {
		i = s.finishRune(p, i)
		if s.un > 0 {
			return i
		}
	}
	for i < len(p) {
		b := p[i]
		switch {
		case b >= 0x20 && b < 0x7f:
			j := i + 1
			for j < len(p) && p[j] >= 0x20 && p[j] < 0x7f {
				j++
			}
			s.printASCII(p[i:j])
			s.out = append(s.out, p[i:j]...)
			i = j
		case b == esc:
			s.seq = append(s.seq[:0], esc)
			s.state = stEsc
			return i + 1
		case b < 0x20:
			s.control(b)
			i++
		case b == 0x7f:
			s.out = append(s.out, b) // DEL is ignored by terminals
			i++
		default:
			if !utf8.FullRune(p[i:]) {
				s.un = copy(s.ubuf[:], p[i:])
				return len(p)
			}
			r, n := utf8.DecodeRune(p[i:])
			s.printRune(r)
			s.out = append(s.out, p[i:i+n]...)
			i += n
		}
	}
	return i
}

// finishRune completes a UTF-8 sequence whose first bytes arrived in an
// earlier Feed. Invalid sequences decode to U+FFFD one byte at a time, exactly
// as utf8.DecodeRune would have treated them in one piece.
func (s *Screen) finishRune(p []byte, i int) int {
	for s.un > 0 {
		var tmp [utf8.UTFMax]byte
		n := copy(tmp[:], s.ubuf[:s.un])
		k := copy(tmp[n:], p[i:])
		buf := tmp[:n+k]
		if !utf8.FullRune(buf) {
			s.un = copy(s.ubuf[:], buf)
			return i + k
		}
		r, size := utf8.DecodeRune(buf)
		s.printRune(r)
		s.out = append(s.out, buf[:size]...)
		if size >= n {
			i += size - n
			s.un = 0
		} else {
			s.un = copy(s.ubuf[:], s.ubuf[size:n])
		}
	}
	return i
}

// decGraphics maps 0x5F–0x7E to the DEC Special Graphics glyphs. Cells store
// the Unicode result, so Capture shows box drawing rather than "lqqk".
var decGraphics = [32]rune{
	0x00A0, // _ blank
	0x25C6, // ` diamond
	0x2592, // a checkerboard
	0x2409, // b HT
	0x240C, // c FF
	0x240D, // d CR
	0x240A, // e LF
	0x00B0, // f degree
	0x00B1, // g plus/minus
	0x2424, // h NL
	0x240B, // i VT
	0x2518, // j ┘
	0x2510, // k ┐
	0x250C, // l ┌
	0x2514, // m └
	0x253C, // n ┼
	0x23BA, // o scan line 1
	0x23BB, // p scan line 3
	0x2500, // q ─
	0x23BC, // r scan line 7
	0x23BD, // s scan line 9
	0x251C, // t ├
	0x2524, // u ┤
	0x2534, // v ┴
	0x252C, // w ┬
	0x2502, // x │
	0x2264, // y ≤
	0x2265, // z ≥
	0x03C0, // { π
	0x2260, // | ≠
	0x00A3, // } £
	0x00B7, // ~ centered dot
}

// printASCII prints a run of printable ASCII bytes, translating through DEC
// Special Graphics when that set is invoked into GL. The common case (no
// pending wrap, replace mode, ASCII set) writes everything that fits before
// the last column in one loop; the last column, with its pending-wrap rule,
// and everything unusual goes through printGlyph.
func (s *Screen) printASCII(run []byte) {
	for len(run) > 0 {
		x := s.cx
		n := min(len(run), s.cols-1-x)
		if n <= 0 || s.pendingWrap || s.modes.Insert || s.g[s.gl] == '0' {
			r := rune(run[0])
			if s.g[s.gl] == '0' && r >= 0x5f {
				r = decGraphics[r-0x5f]
			}
			s.printGlyph(r, 1)
			run = run[1:]
			continue
		}
		ln := &s.buf.lines[s.cy]
		cells := ln.cells
		// Only the ends of the run can cut a wide glyph in half; glyphs in
		// the middle are overwritten whole.
		if cells[x].flags&cellPad != 0 {
			splitWide(cells, x)
		}
		if cells[x+n-1].flags&cellWide != 0 {
			splitWide(cells, x+n-1)
		}
		pen := s.pen
		for i, b := range run[:n] {
			cells[x+i] = cell{r: rune(b), a: pen}
		}
		ln.used = true
		s.cx = x + n
		s.lastRune, s.lastWidth = rune(run[n-1]), 1
		run = run[n:]
	}
}

// control executes a C0 control character (other than ESC, CAN and SUB,
// which the state machine handles) and forwards it, except ENQ.
func (s *Screen) control(b byte) {
	s.lastRune = 0
	switch b {
	case 0x05: // ENQ: the answerback would be typed into the program; drop it
		return
	case 0x08: // BS
		s.backspace()
	case 0x09: // HT
		s.tabForward(1)
	case 0x0a, 0x0b, 0x0c: // LF, VT, FF
		s.pendingWrap = false
		s.index()
	case 0x0d: // CR
		s.carriageReturn()
	case 0x0e: // SO: invoke G1
		s.gl = 1
	case 0x0f: // SI: invoke G0
		s.gl = 0
	}
	s.out = append(s.out, b)
}

// abortSeq forwards the bytes of an unfinished sequence unchanged and returns
// to the ground state. Terminals that see the same bytes recover the same way.
func (s *Screen) abortSeq() {
	if !s.csiOverflow && !s.strOverflow {
		s.out = append(s.out, s.seq...)
	}
	s.seq = s.seq[:0]
	s.csiOverflow, s.strOverflow = false, false
	s.state = stGround
}

// passSeq forwards the completed sequence unchanged.
func (s *Screen) passSeq() { s.out = append(s.out, s.seq...) }

// step consumes one byte in an escape or CSI state. It returns false when the
// byte must be processed again in the (new) current state.
func (s *Screen) step(b byte) bool {
	switch s.state {
	case stEsc:
		return s.escByte(b)
	case stEscInter:
		return s.escInterByte(b)
	case stCSI:
		return s.csiByte(b)
	case stStringEsc:
		return s.stringEscByte(b)
	}
	s.state = stGround
	return false
}

// commonC0 handles the bytes that interrupt any escape sequence: ESC
// restarts it, CAN/SUB cancel it, other C0 controls execute immediately (and
// are forwarded ahead of the held sequence bytes, which a terminal treats the
// same way). It reports whether b was one of them.
func (s *Screen) commonC0(b byte) bool {
	switch {
	case b == esc:
		s.abortSeq()
		s.seq = append(s.seq, esc)
		s.state = stEsc
	case b == can || b == sub:
		s.abortSeq()
		s.out = append(s.out, b)
	case b < 0x20:
		s.control(b)
	default:
		return false
	}
	return true
}

func (s *Screen) escByte(b byte) bool {
	if s.commonC0(b) {
		return true
	}
	switch {
	case b == 0x7f:
		s.seq = append(s.seq, b)
	case b >= 0x20 && b <= 0x2f:
		s.seq = append(s.seq, b)
		s.state = stEscInter
	case b == '[':
		s.seq = append(s.seq, b)
		s.state = stCSI
		s.csiOverflow = false
	case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
		s.seq = append(s.seq, b)
		s.strKind = b
		s.strOverflow = false
		s.state = stString
	case b >= 0x30 && b <= 0x7e:
		s.seq = append(s.seq, b)
		s.state = stGround
		s.dispatchEsc(b)
		s.seq = s.seq[:0]
	default: // bytes >= 0x80 cannot continue an escape sequence
		s.abortSeq()
		return false
	}
	return true
}

func (s *Screen) escInterByte(b byte) bool {
	if s.commonC0(b) {
		return true
	}
	switch {
	case b == 0x7f:
		s.seq = append(s.seq, b)
	case b >= 0x20 && b <= 0x2f:
		if len(s.seq) >= 16 {
			s.abortSeq()
			return false
		}
		s.seq = append(s.seq, b)
	case b >= 0x30 && b <= 0x7e:
		s.seq = append(s.seq, b)
		s.state = stGround
		s.dispatchEscInter()
		s.seq = s.seq[:0]
	default:
		s.abortSeq()
		return false
	}
	return true
}

func (s *Screen) csiByte(b byte) bool {
	if s.commonC0(b) {
		return true
	}
	switch {
	case b >= 0x20 && b <= 0x3f, b == 0x7f:
		if s.csiOverflow {
			s.out = append(s.out, b)
			return true
		}
		s.seq = append(s.seq, b)
		if len(s.seq) > maxCSI {
			s.out = append(s.out, s.seq...)
			s.seq = s.seq[:0]
			s.csiOverflow = true
		}
	case b >= 0x40 && b <= 0x7e:
		s.state = stGround
		if s.csiOverflow {
			s.out = append(s.out, b)
			s.csiOverflow = false
			return true
		}
		s.seq = append(s.seq, b)
		s.lastRuneBeforeCSI()
		if s.dispatchCSI() {
			s.passSeq()
		}
		s.seq = s.seq[:0]
	default:
		s.abortSeq()
		return false
	}
	return true
}

// lastRuneBeforeCSI clears the REP memory for every CSI except REP itself,
// which needs it.
func (s *Screen) lastRuneBeforeCSI() {
	if s.seq[len(s.seq)-1] != 'b' {
		s.lastRune = 0
	}
}

// stringBytes consumes string content in bulk up to the next byte that can
// end or interrupt the string.
func (s *Screen) stringBytes(p []byte, i int) int {
	j := i
	for j < len(p) {
		c := p[j]
		if c == esc || c == bel || c == can || c == sub {
			break
		}
		j++
	}
	s.stringAppend(p[i:j])
	if j == len(p) {
		return j
	}
	switch c := p[j]; c {
	case bel:
		switch {
		case s.strKind != ']':
			s.stringAppend(p[j : j+1]) // BEL is data in DCS/SOS/PM/APC
		case s.strOverflow:
			s.out = append(s.out, bel)
			s.endPassthroughString()
		default:
			s.seq = append(s.seq, bel)
			s.finishString(false)
		}
	case can, sub:
		s.abortSeq()
		s.out = append(s.out, c)
	case esc:
		if s.strOverflow {
			s.seq = append(s.seq[:0], esc) // hold: ST, or the start of a new sequence
		} else {
			s.seq = append(s.seq, esc)
		}
		s.state = stStringEsc
	}
	return j + 1
}

// endPassthroughString ends a string that outgrew maxString; its bytes,
// terminator included, have already been forwarded.
func (s *Screen) endPassthroughString() {
	s.seq = s.seq[:0]
	s.strOverflow = false
	s.state = stGround
}

// stringAppend adds string content, switching to pass-through once the
// buffered string would exceed maxString.
func (s *Screen) stringAppend(data []byte) {
	if len(data) == 0 {
		return
	}
	if s.strOverflow {
		s.out = append(s.out, data...)
		return
	}
	if len(s.seq)+len(data) > maxString {
		s.out = append(s.out, s.seq...)
		s.out = append(s.out, data...)
		s.seq = s.seq[:0]
		s.strOverflow = true
		return
	}
	s.seq = append(s.seq, data...)
}

func (s *Screen) stringEscByte(b byte) bool {
	if b == '\\' {
		if s.strOverflow {
			s.out = append(s.out, esc, '\\')
			s.endPassthroughString()
			return true
		}
		s.seq = append(s.seq, b)
		s.finishString(true)
		return true
	}
	// Any other byte after ESC aborts the string; the ESC starts a new
	// sequence. The aborted string is forwarded untouched and not acted on.
	if !s.strOverflow {
		s.out = append(s.out, s.seq[:len(s.seq)-1]...)
	}
	s.seq = append(s.seq[:0], esc)
	s.strOverflow = false
	s.state = stEsc
	return false
}

// finishString dispatches a complete, buffered string sequence. st reports
// whether it ended with ST (ESC \) rather than BEL.
func (s *Screen) finishString(st bool) {
	s.state = stGround
	s.lastRune = 0
	term := 1
	if st {
		term = 2
	}
	payload := s.seq[2 : len(s.seq)-term]
	pass := true
	switch s.strKind {
	case ']':
		pass = s.osc(payload, st)
	case 'P':
		pass = s.dcs(payload)
	case '_':
		pass = !isKittyGraphicsQuery(payload)
	}
	if pass {
		s.passSeq()
	}
	s.seq = s.seq[:0]
}

// ---------------------------------------------------------------------------
// ESC dispatch

func (s *Screen) dispatchEsc(final byte) {
	s.lastRune = 0
	switch final {
	case '7': // DECSC
		s.saveCursor()
	case '8': // DECRC
		s.restoreCursor()
	case 'D': // IND
		s.pendingWrap = false
		s.index()
	case 'E': // NEL
		s.carriageReturn()
		s.index()
	case 'M': // RI
		s.pendingWrap = false
		s.reverseIndex()
	case 'c': // RIS
		// A full reset would also take the human's terminal off the
		// alternate screen it is kept on while attached, so it is applied
		// here and replaced by a repaint of the reset state.
		s.fullReset()
		s.out = s.appendRender(s.out)
		return
	case '=': // DECKPAM
		s.modes.AppKeypad = true
	case '>': // DECKPNM
		s.modes.AppKeypad = false
	case 'H': // HTS
		s.tabs[s.cx] = true
	case 'n': // LS2
		s.gl = 2
	case 'o': // LS3
		s.gl = 3
	case 'Z': // DECID: an obsolete form of DA1
		s.reply = append(s.reply, da1Reply...)
		return
	}
	s.passSeq()
}

func (s *Screen) dispatchEscInter() {
	s.lastRune = 0
	inter := s.seq[1 : len(s.seq)-1]
	final := s.seq[len(s.seq)-1]
	if len(inter) == 1 {
		switch inter[0] {
		case '(', ')', '*', '+': // designate G0..G3 (94-character sets)
			set := byte('B')
			if final == '0' {
				set = '0'
			}
			s.g[inter[0]-'('] = set
		case '-', '.', '/': // designate G1..G3 (96-character sets): treat as ASCII
			s.g[inter[0]-','] = 'B'
		case '#':
			if final == '8' {
				s.alignmentTest()
			}
		}
	}
	s.passSeq()
}

// ---------------------------------------------------------------------------
// OSC, DCS, APC

// Replies for the dynamic color queries: a light gray foreground and cursor on
// a black background, which is what most programs assume when they cannot
// tell (a dark theme).
var oscColorReplies = map[int]string{
	10: "rgb:c0c0/c0c0/c0c0",
	11: "rgb:0000/0000/0000",
	12: "rgb:c0c0/c0c0/c0c0",
}

// osc handles a complete OSC payload (between "ESC ]" and the terminator) and
// reports whether the sequence should be forwarded.
func (s *Screen) osc(payload []byte, st bool) bool {
	k := bytes.IndexByte(payload, ';')
	numPart, rest := payload, []byte(nil)
	if k >= 0 {
		numPart, rest = payload[:k], payload[k+1:]
	}
	num, err := strconv.Atoi(string(numPart))
	if err != nil || len(numPart) == 0 || numPart[0] < '0' || numPart[0] > '9' {
		return true
	}
	switch {
	case num == 0 || num == 2:
		s.title = sanitizeTitle(rest)
	case num == 4:
		// Palette queries "4;n;?" would make the human's terminal type an
		// answer into the program; there is no palette to answer from.
		parts := bytes.Split(rest, []byte{';'})
		for i := 1; i < len(parts); i += 2 {
			if string(parts[i]) == "?" {
				return false
			}
		}
	case num >= 10 && num <= 19:
		// Dynamic colors. "10;?" queries the foreground; xterm also accepts
		// a chain such as "10;?;?" querying 10 and 11.
		parts := bytes.Split(rest, []byte{';'})
		query := false
		for i, part := range parts {
			if string(part) != "?" {
				continue
			}
			query = true
			if v, ok := oscColorReplies[num+i]; ok {
				s.reply = append(s.reply, "\x1b]"...)
				s.reply = strconv.AppendInt(s.reply, int64(num+i), 10)
				s.reply = append(s.reply, ';')
				s.reply = append(s.reply, v...)
				if st {
					s.reply = append(s.reply, esc, '\\')
				} else {
					s.reply = append(s.reply, bel)
				}
			}
		}
		return !query
	case num == 52:
		// "52;c;?" asks the human's terminal to type the clipboard into the
		// program. Writes (base64 data) pass through.
		if j := bytes.IndexByte(rest, ';'); j >= 0 && string(rest[j+1:]) == "?" {
			return false
		}
	}
	return true
}

// sanitizeTitle keeps a title safe to re-emit inside an OSC by Render: valid
// UTF-8 without C0/C1 controls or DEL, at most 1 KiB.
func sanitizeTitle(p []byte) string {
	out := make([]byte, 0, min(len(p), 1024))
	for len(p) > 0 {
		r, n := utf8.DecodeRune(p)
		p = p[n:]
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			continue
		}
		if len(out)+utf8.RuneLen(r) > 1024 {
			break
		}
		out = utf8.AppendRune(out, r)
	}
	return string(out)
}

// dcs handles a complete DCS payload and reports whether to forward it.
// DECRQSS (DCS $ q) and XTGETTCAP (DCS + q) are queries: they are answered
// with "invalid request" so programs fall back to defaults without waiting.
func (s *Screen) dcs(payload []byte) bool {
	k := 0
	for k < len(payload) && payload[k] >= 0x30 && payload[k] <= 0x3f {
		k++
	}
	j := k
	for j < len(payload) && payload[j] >= 0x20 && payload[j] <= 0x2f {
		j++
	}
	if j >= len(payload) || k != 0 || j-k != 1 || payload[j] != 'q' {
		return true
	}
	switch payload[k] {
	case '$':
		s.reply = append(s.reply, "\x1bP0$r\x1b\\"...)
		return false
	case '+':
		s.reply = append(s.reply, "\x1bP0+r\x1b\\"...)
		return false
	}
	return true
}

// isKittyGraphicsQuery reports whether an APC payload is a kitty graphics
// protocol query ("G...a=q..."). A human's kitty would answer it into the
// program's input long after the program, having received our DA1 reply,
// stopped waiting; the stray answer would show up as typed garbage.
func isKittyGraphicsQuery(payload []byte) bool {
	if len(payload) == 0 || payload[0] != 'G' {
		return false
	}
	ctrl := payload[1:]
	if k := bytes.IndexByte(ctrl, ';'); k >= 0 {
		ctrl = ctrl[:k]
	}
	for _, kv := range bytes.Split(ctrl, []byte{','}) {
		if string(kv) == "a=q" {
			return true
		}
	}
	return false
}
