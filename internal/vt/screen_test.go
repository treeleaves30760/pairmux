package vt

import (
	"fmt"
	"strings"
	"testing"
)

// screenLines returns the visible capture as lines (without the final "\n").
func screenLines(s *Screen) []string {
	return strings.Split(strings.TrimSuffix(s.Capture(0), "\n"), "\n")
}

// checkScreen asserts the first len(want) visible rows and that the remaining
// rows are blank.
func checkScreen(t *testing.T, s *Screen, want []string) {
	t.Helper()
	got := screenLines(s)
	for i := range got {
		w := ""
		if i < len(want) {
			w = want[i]
		}
		if got[i] != w {
			t.Errorf("row %d = %q, want %q\nscreen:\n%s", i, got[i], w, s.Capture(0))
			return
		}
	}
	if len(want) > len(got) {
		t.Errorf("want %d rows, screen has %d", len(want), len(got))
	}
}

func feedString(s *Screen, in string) (string, string) {
	fwd, rep := s.Feed([]byte(in))
	return string(fwd), string(rep)
}

type emuCase struct {
	name       string
	cols, rows int
	in         string
	want       []string
	cx, cy     int // expected cursor; skipped when cx < 0
}

func runEmuCases(t *testing.T, cases []emuCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cols, rows := c.cols, c.rows
			if cols == 0 {
				cols = 10
			}
			if rows == 0 {
				rows = 5
			}
			s := New(cols, rows, 100)
			feedString(s, c.in)
			checkScreen(t, s, c.want)
			if c.cx >= 0 {
				if x, y := s.Cursor(); x != c.cx || y != c.cy {
					t.Errorf("cursor = %d,%d, want %d,%d", x, y, c.cx, c.cy)
				}
			}
		})
	}
}

func TestPrintAndWrap(t *testing.T) {
	runEmuCases(t, []emuCase{
		{name: "plain", in: "abc", want: []string{"abc"}, cx: 3, cy: 0},
		{name: "wrap", in: "0123456789X", want: []string{"0123456789", "X"}, cx: 1, cy: 1},
		{name: "pending wrap cursor stays in last column", in: "0123456789", want: []string{"0123456789"}, cx: 9, cy: 0},
		{name: "CR cancels pending wrap", in: "0123456789\rX", want: []string{"X123456789"}, cx: 1, cy: 0},
		{name: "CRLF after full line", in: "0123456789\r\nX", want: []string{"0123456789", "X"}, cx: 1, cy: 1},
		{name: "LF cancels pending wrap (xterm)", in: "0123456789\nX", want: []string{"0123456789", "         X"}, cx: 9, cy: 1},
		{name: "BS from pending wrap", in: "0123456789\bY", want: []string{"01234567Y9"}, cx: 9, cy: 0},
		{name: "CUB from pending wrap", in: "0123456789\x1b[DY", want: []string{"01234567Y9"}, cx: 9, cy: 0},
		{name: "EL from pending wrap erases last glyph", in: "0123456789\x1b[KX", want: []string{"012345678X"}, cx: 9, cy: 0},
		{name: "DECSC keeps pending wrap", in: "0123456789\x1b7\x1b[3;3H\x1b8X", want: []string{"0123456789", "X"}, cx: 1, cy: 1},
		{name: "autowrap off overwrites last column", in: "\x1b[?7l0123456789XYZ", want: []string{"012345678Z"}, cx: 9, cy: 0},
		{name: "autowrap off cancels pending wrap", in: "0123456789\x1b[?7lZ", want: []string{"012345678Z"}, cx: 9, cy: 0},
		{name: "wrap scrolls at bottom", rows: 2, cols: 4, in: "abcdefghij", want: []string{"efgh", "ij"}, cx: 2, cy: 1},
		{name: "insert mode", in: "abcdef\r\x1b[4hXY\x1b[4l", want: []string{"XYabcdef"}, cx: 2, cy: 0},
		{name: "insert mode pushes off the edge", in: "0123456789\r\x1b[4hX", want: []string{"X012345678"}, cx: 1, cy: 0},
		{name: "DEL and NUL are ignored", in: "a\x7fb\x00c\x07", want: []string{"abc"}, cx: 3, cy: 0},
		{name: "invalid UTF-8 shows U+FFFD per byte", in: "a\xffb\xe4\xb8c", want: []string{"a\ufffdb\ufffd\ufffdc"}, cx: 6, cy: 0},
		{name: "C1 controls are not shown", in: "a\xc2\x85b\xc2\x9bc", want: []string{"abc"}, cx: 3, cy: 0},
	})
}

func TestWideAndCombining(t *testing.T) {
	runEmuCases(t, []emuCase{
		{name: "wide", in: "中文", want: []string{"中文"}, cx: 4, cy: 0},
		{name: "wide wraps from last column", cols: 5, in: "abcd中", want: []string{"abcd", "中"}, cx: 2, cy: 1},
		{name: "wide fills to the edge", cols: 4, in: "中文", want: []string{"中文"}, cx: 3, cy: 0},
		{name: "wide then wrap", cols: 4, in: "中文x", want: []string{"中文", "x"}, cx: 1, cy: 1},
		{name: "wide dropped without autowrap", cols: 5, in: "\x1b[?7labcd中", want: []string{"abcd"}, cx: 4, cy: 0},
		{name: "wide never fits one column", cols: 1, in: "中a", want: []string{"a"}, cx: 0, cy: 0},
		{name: "overwrite right half", in: "中文\x1b[1;2Hx", want: []string{" x文"}, cx: 2, cy: 0},
		{name: "overwrite left half of next", in: "中文\x1b[1;4Hx", want: []string{"中 x"}, cx: 4, cy: 0},
		{name: "wide over wide offset", in: "中文\x1b[1;2H測", want: []string{" 測"}, cx: 3, cy: 0},
		{name: "combining", in: "e\u0301x", want: []string{"e\u0301x"}, cx: 2, cy: 0},
		{name: "combining in column 0 dropped", in: "\u0301a", want: []string{"a"}, cx: 1, cy: 0},
		{name: "combining after wide", in: "中\u0301", want: []string{"中\u0301"}, cx: 2, cy: 0},
		{name: "combining on wide right half dropped", in: "中\x1b[D\u0301", want: []string{"中"}, cx: 1, cy: 0},
		{name: "combining after cursor move", in: "ab\x1b[D\u0301", want: []string{"a\u0301b"}, cx: 1, cy: 0},
		{name: "combining on blank cell", in: "\x1b[3C\u0301x", want: []string{"   \u0301x"}, cx: 4, cy: 0},
		{name: "combining at pending wrap", in: "0123456789\u0301", want: []string{"012345678" + "9\u0301"}, cx: 9, cy: 0},
		{name: "zero width format chars", in: "a\u200bb\u2060c\ufeffd", want: []string{"a\u200bb\u2060c\ufeffd"}, cx: 4, cy: 0},
		{name: "hangul jamo compose", in: "각k", want: []string{"각k"}, cx: 3, cy: 0},
		{name: "VS16 widens", in: "❤\ufe0fx", want: []string{"❤\ufe0fx"}, cx: 3, cy: 0},
		{name: "VS15 does not widen", in: "❤\ufe0ex", want: []string{"❤\ufe0ex"}, cx: 2, cy: 0},
		{name: "VS16 on wide stays wide", in: "中\ufe0f", want: []string{"中\ufe0f"}, cx: 2, cy: 0},
		{name: "VS16 widen claims next cell", in: "ab\x1b[D\ufe0f", want: []string{"a\ufe0f"}, cx: 2, cy: 0},
		{name: "VS16 cannot widen last column", in: "012345678❤\ufe0fQ", want: []string{"012345678❤\ufe0f", "Q"}, cx: 1, cy: 1},
		{name: "VS16 widen to the edge sets pending wrap", in: "01234567❤\ufe0fQ", want: []string{"01234567❤\ufe0f", "Q"}, cx: 1, cy: 1},
		{name: "emoji ZWJ sequence is one glyph", in: "\U0001f468\u200d\U0001f469\u200d\U0001f467x", want: []string{"\U0001f468\u200d\U0001f469\u200d\U0001f467x"}, cx: 3, cy: 0},
		{name: "ASCII after ZWJ is separate", in: "a\u200db", want: []string{"a\u200db"}, cx: 2, cy: 0},
		{name: "non-ASCII after ZWJ joins", in: "a\u200d\U0001f600", want: []string{"a\u200d\U0001f600"}, cx: 1, cy: 0},
		{name: "regional indicator pair", in: "\U0001f1f9\U0001f1fcx", want: []string{"\U0001f1f9\U0001f1fcx"}, cx: 3, cy: 0},
		{name: "regional indicator triple", in: "\U0001f1f9\U0001f1fc\U0001f1ef", want: []string{"\U0001f1f9\U0001f1fc\U0001f1ef"}, cx: 3, cy: 0},
		{name: "emoji modifier is its own glyph", in: "\U0001f44d\U0001f3fd", want: []string{"\U0001f44d\U0001f3fd"}, cx: 4, cy: 0},
		{name: "ICH splits wide", in: "中文\x1b[1;2H\x1b[@", want: []string{"   文"}, cx: 1, cy: 0},
		{name: "ICH pushes wide off edge", cols: 6, in: "abcd中\r\x1b[@", want: []string{" abcd"}, cx: 0, cy: 0},
		{name: "DCH of wide lead", in: "中文\r\x1b[P", want: []string{" 文"}, cx: 0, cy: 0},
		{name: "DCH pulls wide half", in: "a中文\x1b[1;2H\x1b[2P", want: []string{"a文"}, cx: 1, cy: 0},
		{name: "DCH half of wide", in: "中文測\x1b[1;3H\x1b[P", want: []string{"中 測"}, cx: 2, cy: 0},
		{name: "ECH on right half", in: "中文\x1b[1;2H\x1b[X", want: []string{"  文"}, cx: 1, cy: 0},
		{name: "EL on right half", in: "中文\x1b[1;2H\x1b[K", want: []string{""}, cx: 1, cy: 0},
		{name: "EL 1 on left half", in: "中文\x1b[1;3H\x1b[1K", want: []string{""}, cx: 2, cy: 0},
	})
}

func TestControls(t *testing.T) {
	runEmuCases(t, []emuCase{
		{name: "BS", in: "abc\b\bX", want: []string{"aXc"}, cx: 2, cy: 0},
		{name: "BS at column 0", in: "\b\bX", want: []string{"X"}, cx: 1, cy: 0},
		{name: "BS does not reverse wrap", in: "0123456789ab\r\b\bX", want: []string{"0123456789", "Xb"}, cx: 1, cy: 1},
		{name: "LF VT FF", in: "a\nb\x0bc\x0cd", want: []string{"a", " b", "  c", "   d"}, cx: 4, cy: 3},
		{name: "LF scrolls", rows: 2, in: "a\r\nb\r\nc", want: []string{"b", "c"}, cx: 1, cy: 1},
		{name: "HT", cols: 20, in: "a\tb\tc", want: []string{"a       b       c"}, cx: 17, cy: 0},
		{name: "HT stops at last column", cols: 12, in: "a\t\t\tb", want: []string{"a          b"}, cx: 11, cy: 0},
		{name: "HT at pending wrap does nothing", cols: 4, in: "abcd\tX", want: []string{"abcd", "X"}, cx: 1, cy: 1},
		{name: "SO SI with G1 graphics", in: "\x1b)0a\x0eqx\x0fq", want: []string{"a─│q"}, cx: 4, cy: 0},
		{name: "G0 graphics", in: "\x1b(0lqk\x1b(Bq", want: []string{"┌─┐q"}, cx: 4, cy: 0},
		{name: "all DEC graphics", cols: 40, in: "\x1b(0_`abcdefghijklmnopqrstuvwxyz{|}~\x1b(B",
			want: []string{" ◆▒␉␌␍␊°±␤␋┘┐┌└┼⎺⎻─⎼⎽├┤┴┬│≤≥π≠£·"}, cx: 32, cy: 0},
		{name: "UK set is treated as ASCII", in: "\x1b(Aq", want: []string{"q"}, cx: 1, cy: 0},
		{name: "LS2 with G2 graphics", in: "\x1b*0\x1bnq\x1b(B\x1b(Bq\x0fq", want: []string{"──q"}, cx: 3, cy: 0},
		{name: "ENQ ignored", in: "a\x05b", want: []string{"ab"}, cx: 2, cy: 0},
		{name: "CAN aborts sequence", in: "\x1b[3\x18A", want: []string{"A"}, cx: 1, cy: 0},
		{name: "C0 inside CSI executes", in: "abc\x1b[\r2CX", want: []string{"abX"}, cx: 3, cy: 0},
		{name: "ESC restarts sequence", in: "\x1b[2\x1b[3CX", want: []string{"   X"}, cx: 4, cy: 0},
	})
}

func TestTabStops(t *testing.T) {
	runEmuCases(t, []emuCase{
		{name: "HTS and TBC", cols: 20, in: "\x1b[3g\x1b[1;4H\x1bH\r\tA\tB", want: []string{"   A               B"}, cx: 19, cy: 0},
		{name: "TBC 0 clears one", cols: 20, in: "\x1b[1;9H\x1b[g\r\tA", want: []string{"                A"}, cx: 17, cy: 0},
		{name: "CHT", cols: 30, in: "\x1b[2IA", want: []string{"                A"}, cx: 17, cy: 0},
		{name: "CBT", cols: 30, in: "\x1b[1;20H\x1b[ZA\x1b[2ZB", want: []string{"        B       A"}, cx: 9, cy: 0},
		{name: "DECST8C", cols: 30, in: "\x1b[3g\x1b[?5W\tA", want: []string{"        A"}, cx: 9, cy: 0},
	})
}

func TestEscapes(t *testing.T) {
	runEmuCases(t, []emuCase{
		{name: "IND", in: "ab\x1bDc", want: []string{"ab", "  c"}, cx: 3, cy: 1},
		{name: "NEL", in: "ab\x1bEc", want: []string{"ab", "c"}, cx: 1, cy: 1},
		{name: "RI", in: "\r\n\r\nab\x1bMc", want: []string{"", "  c", "ab"}, cx: 3, cy: 1},
		{name: "RI scrolls at top", rows: 3, in: "a\r\nb\x1b[H\x1bMc", want: []string{"c", "a", "b"}, cx: 1, cy: 0},
		{name: "DECSC DECRC", in: "\x1b[2;3H\x1b7\x1b[5;5H\x1b8X", want: []string{"", "  X"}, cx: 3, cy: 1},
		{name: "DECRC without save homes", in: "\x1b[3;3H\x1b8X", want: []string{"X"}, cx: 1, cy: 0},
		{name: "DECSC saves charset", in: "\x1b(0\x1b7\x1b(B\x1b8q", want: []string{"─"}, cx: 1, cy: 0},
		{name: "SCOSC SCORC", in: "\x1b[2;3H\x1b[s\x1b[5;5H\x1b[uX", want: []string{"", "  X"}, cx: 3, cy: 1},
		{name: "DECALN", cols: 3, rows: 2, in: "\x1b#8", want: []string{"EEE", "EEE"}, cx: 0, cy: 0},
		{name: "unknown escapes ignored", in: "\x1b#3\x1bNa\x1b%Gb\x1b Fc", want: []string{"abc"}, cx: 3, cy: 0},
		{name: "RIS", in: "abc\x1b[?1049h\x1b[31mxyz\x1bcq", want: []string{"q"}, cx: 1, cy: 0},
	})
}

func TestCursorMovement(t *testing.T) {
	runEmuCases(t, []emuCase{
		{name: "CUP", in: "\x1b[3;4HX", want: []string{"", "", "   X"}, cx: 4, cy: 2},
		{name: "CUP defaults", in: "\x1b[3;4H\x1b[HX", want: []string{"X"}, cx: 1, cy: 0},
		{name: "CUP clamps", in: "\x1b[99;99HX", want: []string{"", "", "", "", "         X"}, cx: 9, cy: 4},
		{name: "HVP", in: "\x1b[2;2fX", want: []string{"", " X"}, cx: 2, cy: 1},
		{name: "CUU CUD CUF CUB", in: "\x1b[3;5H\x1b[2AA\x1b[3BB\x1b[2CC\x1b[6DD", want: []string{"    A", "", "", "   D B  C"}, cx: 4, cy: 3},
		{name: "zero counts are one", in: "\x1b[3;5H\x1b[0A\x1b[0DX", want: []string{"", "   X"}, cx: 4, cy: 1},
		{name: "CNL CPL", in: "\x1b[2;5H\x1b[2Ea\x1b[1Fb", want: []string{"", "", "b", "a"}, cx: 1, cy: 2},
		{name: "CHA HPA", in: "\x1b[5GA\x1b[2`B", want: []string{" B  A"}, cx: 2, cy: 0},
		{name: "VPA", in: "\x1b[1;3H\x1b[4dX", want: []string{"", "", "", "  X"}, cx: 3, cy: 3},
		{name: "VPR HPR", in: "\x1b[2e\x1b[3aX", want: []string{"", "", "   X"}, cx: 4, cy: 2},
		{name: "CUU stops at top margin", in: "\x1b[2;4r\x1b[3;1H\x1b[9AX", want: []string{"", "X"}, cx: 1, cy: 1},
		{name: "CUU above region goes to row 0", in: "\x1b[3;4r\x1b[2;1H\x1b[9AX", want: []string{"X"}, cx: 1, cy: 0},
		{name: "CUD stops at bottom margin", in: "\x1b[2;3r\x1b[2;1H\x1b[9BX", want: []string{"", "", "X"}, cx: 1, cy: 2},
		{name: "CUD below region goes to last row", in: "\x1b[1;2r\x1b[3;1H\x1b[9BX", want: []string{"", "", "", "", "X"}, cx: 1, cy: 4},
		{name: "origin mode", in: "\x1b[2;4r\x1b[?6h\x1b[1;1HA\x1b[9;1HB\x1b[2dC", want: []string{"", "A", " C", "B"}, cx: 2, cy: 2},
		{name: "origin mode off homes", in: "\x1b[2;4r\x1b[?6h\x1b[?6lX", want: []string{"X"}, cx: 1, cy: 0},
		{name: "DECSTBM homes", in: "\x1b[3;3H\x1b[2;4rX", want: []string{"X"}, cx: 1, cy: 0},
		{name: "DECSTBM homes into region in origin mode", in: "\x1b[?6h\x1b[2;4rX", want: []string{"", "X"}, cx: 1, cy: 1},
		{name: "DECSTBM invalid ignored", in: "\x1b[3;3H\x1b[4;2r\x1b[3;3rX", want: []string{"", "", "  X"}, cx: 3, cy: 2},
	})
}

func TestEditing(t *testing.T) {
	fill := "AAAAAAAAAA\x1b[2;1HBBBBBBBBBB\x1b[3;1HCCCCCCCCCC\x1b[4;1HDDDDDDDDDD\x1b[5;1HEEEEEEEEEE"
	runEmuCases(t, []emuCase{
		{name: "ED 0", in: fill + "\x1b[3;5H\x1b[J", want: []string{"AAAAAAAAAA", "BBBBBBBBBB", "CCCC"}, cx: 4, cy: 2},
		{name: "ED 1", in: fill + "\x1b[3;5H\x1b[1J", want: []string{"", "", "     CCCCC", "DDDDDDDDDD", "EEEEEEEEEE"}, cx: 4, cy: 2},
		{name: "ED 2", in: fill + "\x1b[3;5H\x1b[2J", want: []string{}, cx: 4, cy: 2},
		{name: "EL 0", in: fill + "\x1b[1;4H\x1b[K", want: []string{"AAA", "BBBBBBBBBB", "CCCCCCCCCC", "DDDDDDDDDD", "EEEEEEEEEE"}, cx: 3, cy: 0},
		{name: "EL 1", in: fill + "\x1b[1;4H\x1b[1K", want: []string{"    AAAAAA", "BBBBBBBBBB", "CCCCCCCCCC", "DDDDDDDDDD", "EEEEEEEEEE"}, cx: 3, cy: 0},
		{name: "EL 2", in: fill + "\x1b[1;4H\x1b[2K", want: []string{"", "BBBBBBBBBB", "CCCCCCCCCC", "DDDDDDDDDD", "EEEEEEEEEE"}, cx: 3, cy: 0},
		{name: "ECH", in: "abcdef\x1b[1;2H\x1b[3X", want: []string{"a   ef"}, cx: 1, cy: 0},
		{name: "ECH past edge", in: "abcdef\x1b[1;2H\x1b[99X", want: []string{"a"}, cx: 1, cy: 0},
		{name: "ICH", in: "abcdef\x1b[1;2H\x1b[2@", want: []string{"a  bcdef"}, cx: 1, cy: 0},
		{name: "ICH past edge", in: "abcdef\x1b[1;2H\x1b[99@", want: []string{"a"}, cx: 1, cy: 0},
		{name: "DCH", in: "abcdef\x1b[1;2H\x1b[2P", want: []string{"adef"}, cx: 1, cy: 0},
		{name: "IL", in: fill + "\x1b[2;3H\x1b[2L", want: []string{"AAAAAAAAAA", "", "", "BBBBBBBBBB", "CCCCCCCCCC"}, cx: 2, cy: 1},
		{name: "DL", in: fill + "\x1b[2;3H\x1b[2M", want: []string{"AAAAAAAAAA", "DDDDDDDDDD", "EEEEEEEEEE"}, cx: 2, cy: 1},
		{name: "IL in region", in: fill + "\x1b[2;4r\x1b[3;1H\x1b[L", want: []string{"AAAAAAAAAA", "BBBBBBBBBB", "", "CCCCCCCCCC", "EEEEEEEEEE"}, cx: 0, cy: 2},
		{name: "DL in region", in: fill + "\x1b[2;4r\x1b[2;1H\x1b[M", want: []string{"AAAAAAAAAA", "CCCCCCCCCC", "DDDDDDDDDD", "", "EEEEEEEEEE"}, cx: 0, cy: 1},
		{name: "IL large count", in: fill + "\x1b[2;1H\x1b[99L", want: []string{"AAAAAAAAAA"}, cx: 0, cy: 1},
		{name: "IL below region (tmux)", in: fill + "\x1b[1;2r\x1b[4;1H\x1b[L", want: []string{"AAAAAAAAAA", "BBBBBBBBBB", "CCCCCCCCCC", "", "DDDDDDDDDD"}, cx: 0, cy: 3},
		{name: "SU", in: fill + "\x1b[2S", want: []string{"CCCCCCCCCC", "DDDDDDDDDD", "EEEEEEEEEE"}, cx: 9, cy: 4},
		{name: "SD", in: fill + "\x1b[2T", want: []string{"", "", "AAAAAAAAAA", "BBBBBBBBBB", "CCCCCCCCCC"}, cx: 9, cy: 4},
		{name: "SU SD in region", in: fill + "\x1b[2;4r\x1b[S\x1b[2T", want: []string{"AAAAAAAAAA", "", "", "CCCCCCCCCC", "EEEEEEEEEE"}, cx: 0, cy: 0},
		{name: "LF in region", in: fill + "\x1b[2;3r\x1b[3;1H\nX", want: []string{"AAAAAAAAAA", "CCCCCCCCCC", "X", "DDDDDDDDDD", "EEEEEEEEEE"}, cx: 1, cy: 2},
		{name: "LF below region at bottom", in: fill + "\x1b[1;2r\x1b[5;1H\nX", want: []string{"AAAAAAAAAA", "BBBBBBBBBB", "CCCCCCCCCC", "DDDDDDDDDD", "XEEEEEEEEE"}, cx: 1, cy: 4},
		{name: "REP", in: "ab\x1b[3b", want: []string{"abbbb"}, cx: 5, cy: 0},
		{name: "REP wraps", cols: 4, in: "a\x1b[5b", want: []string{"aaaa", "aa"}, cx: 2, cy: 1},
		{name: "REP after control does nothing", in: "a\r\x1b[3b", want: []string{"a"}, cx: 0, cy: 0},
		{name: "REP of DEC graphics", in: "\x1b(0q\x1b[2b", want: []string{"───"}, cx: 3, cy: 0},
		{name: "SD with many params is mouse tracking", in: fill + "\x1b[1;2;3;4;5T", want: []string{"AAAAAAAAAA", "BBBBBBBBBB", "CCCCCCCCCC", "DDDDDDDDDD", "EEEEEEEEEE"}, cx: 9, cy: 4},
		{name: "DECSED DECSEL", in: fill + "\x1b[2;3H\x1b[?K\x1b[4;1H\x1b[?J", want: []string{"AAAAAAAAAA", "BB", "CCCCCCCCCC"}, cx: 0, cy: 3},
	})
}

func TestSGR(t *testing.T) {
	idx := func(i int) color { return colorIndexed | color(i) }
	rgb := func(r, g, b int) color { return colorRGB | color(r<<16|g<<8|b) }
	cases := []struct {
		in   string
		want attr
	}{
		{"\x1b[1;31m", attr{flags: attrBold, fg: idx(1)}},
		{"\x1b[1;31m\x1b[m", attr{}},
		{"\x1b[1;31m\x1b[0m", attr{}},
		{"\x1b[2;3;5;7;8;9;53m", attr{flags: attrDim | attrItalic | attrBlink | attrReverse | attrInvisible | attrStrike | attrOverline}},
		{"\x1b[1;2;3;5;7;8;9;53m\x1b[22;23;25;27;28;29;55m", attr{}},
		{"\x1b[4m", attr{ul: 1}},
		{"\x1b[4:3m", attr{ul: 3}},
		{"\x1b[4:0m", attr{}},
		{"\x1b[21m", attr{ul: 2}},
		{"\x1b[4m\x1b[24m", attr{}},
		{"\x1b[6m", attr{flags: attrBlink}},
		{"\x1b[38;5;200;48;5;17m", attr{fg: idx(200), bg: idx(17)}},
		{"\x1b[38;2;1;2;3m", attr{fg: rgb(1, 2, 3)}},
		{"\x1b[38:2::1:2:3m", attr{fg: rgb(1, 2, 3)}},
		{"\x1b[38:2:1:2:3m", attr{fg: rgb(1, 2, 3)}},
		{"\x1b[48:5:9m", attr{bg: idx(9)}},
		{"\x1b[58:2::4:5:6m", attr{ulc: rgb(4, 5, 6)}},
		{"\x1b[58;5;3m\x1b[59m", attr{}},
		{"\x1b[90;107m", attr{fg: idx(8), bg: idx(15)}},
		{"\x1b[31;41m\x1b[39;49m", attr{}},
		{"\x1b[38;5m\x1b[1m", attr{flags: attrBold}},    // truncated: rest of that SGR ignored
		{"\x1b[38;5;300;1m", attr{flags: attrBold}},     // out-of-range index ignored
		{"\x1b[38;2;1;2;999;1m", attr{flags: attrBold}}, // out-of-range component ignored
		{"\x1b[;1m", attr{flags: attrBold}},             // empty parameter is 0
		{"\x1b[1m\x1b[>4;1m", attr{flags: attrBold}},    // XTMODKEYS is not SGR
		{"\x1b[31m\x1b[38;2;10;20;30;1m", attr{flags: attrBold, fg: rgb(10, 20, 30)}},
	}
	for _, c := range cases {
		s := New(10, 2, 0)
		feedString(s, c.in)
		if s.pen != c.want {
			t.Errorf("%q: pen = %+v, want %+v", c.in, s.pen, c.want)
		}
	}
	// Erases use the current background only (background color erase).
	s := New(4, 1, 0)
	feedString(s, "ab\x1b[1;4;41m\x1b[1K")
	want := cell{a: attr{bg: idx(1)}}
	if got := s.buf.lines[0].cells[0]; got != want {
		t.Errorf("erased cell = %+v, want %+v", got, want)
	}
}

func TestModes(t *testing.T) {
	s := New(10, 5, 0)
	if m := s.Modes(); !m.Autowrap || !m.CursorVisible || m != (Modes{Autowrap: true, CursorVisible: true}) {
		t.Fatalf("initial modes = %+v", m)
	}
	feedString(s, "\x1b[?1;25l\x1b[?1h\x1b=\x1b[?2004h\x1b[?1004h\x1b[?1002h\x1b[?1006h\x1b[?7l\x1b[4h\x1b[?6h")
	want := Modes{AppCursor: true, AppKeypad: true, BracketedPaste: true, FocusEvents: true,
		MouseTracking: 1002, MouseEncoding: 1006, Insert: true, OriginMode: true}
	if m := s.Modes(); m != want {
		t.Errorf("modes = %+v\nwant    %+v", m, want)
	}
	feedString(s, "\x1b[?1000h\x1b[?1015h")
	if m := s.Modes(); m.MouseTracking != 1000 || m.MouseEncoding != 1015 {
		t.Errorf("mouse = %d/%d", m.MouseTracking, m.MouseEncoding)
	}
	feedString(s, "\x1b[?1003l\x1b[?1005l")
	if m := s.Modes(); m.MouseTracking != 0 || m.MouseEncoding != 0 {
		t.Errorf("any mouse reset turns tracking off: %d/%d", m.MouseTracking, m.MouseEncoding)
	}
	feedString(s, "\x1b>\x1b[?66h")
	if !s.Modes().AppKeypad {
		t.Error("DECNKM should set AppKeypad")
	}
	feedString(s, "\x1b[2 q")
	if s.cursorStyle != 2 {
		t.Errorf("cursor style = %d", s.cursorStyle)
	}
	feedString(s, "\x1b[!p")
	m := s.Modes()
	if !m.CursorVisible || m.Insert || m.OriginMode || m.AppCursor || m.AppKeypad || !m.Autowrap {
		t.Errorf("after DECSTR modes = %+v", m)
	}
	if !m.BracketedPaste || !m.FocusEvents {
		t.Errorf("DECSTR must keep other modes: %+v", m)
	}
}

func TestAltScreen(t *testing.T) {
	t.Run("1049", func(t *testing.T) {
		s := New(10, 3, 100)
		feedString(s, "main\r\nline2\x1b[?1049h")
		if !s.Modes().AltScreen {
			t.Fatal("not on alt screen")
		}
		checkScreen(t, s, nil)
		if x, y := s.Cursor(); x != 5 || y != 1 {
			t.Errorf("cursor moved on switch: %d,%d", x, y)
		}
		feedString(s, "\x1b[Halt\r\n1\r\n2\r\n3\r\n4")
		checkScreen(t, s, []string{"2", "3", "4"})
		if s.hist.len() != 0 {
			t.Errorf("alternate screen scrolled into history: %d", s.hist.len())
		}
		if got := s.Capture(10); got != "2\n3\n4\n" {
			t.Errorf("alt capture includes history: %q", got)
		}
		feedString(s, "\x1b[?1049l")
		checkScreen(t, s, []string{"main", "line2"})
		if x, y := s.Cursor(); x != 5 || y != 1 {
			t.Errorf("cursor not restored: %d,%d", x, y)
		}
		feedString(s, "\x1b[?1049h")
		checkScreen(t, s, nil) // 1049 clears on entry
	})
	t.Run("47 keeps contents", func(t *testing.T) {
		s := New(10, 3, 100)
		feedString(s, "main\x1b[?47hALT\x1b[?47l")
		checkScreen(t, s, []string{"main"})
		feedString(s, "\x1b[?47h")
		checkScreen(t, s, []string{"    ALT"})
	})
	t.Run("1047 clears on exit", func(t *testing.T) {
		s := New(10, 3, 100)
		feedString(s, "main\x1b[?1047hALT\x1b[?1047l\x1b[?1047h")
		checkScreen(t, s, nil)
	})
	t.Run("separate DECSC slots", func(t *testing.T) {
		s := New(10, 3, 100)
		feedString(s, "\x1b[2;2H\x1b7\x1b[?1049h\x1b[3;3H\x1b7\x1b[H\x1b8X\x1b[?1049l\x1b8Y")
		checkScreen(t, s, []string{"", " Y"})
	})
	t.Run("1049l on main restores cursor", func(t *testing.T) {
		s := New(10, 3, 100)
		feedString(s, "\x1b[2;2H\x1b[?1049h\x1b[?1049l\x1b[3;3H\x1b[?1049lX")
		checkScreen(t, s, []string{"", " X"})
	})
}

func TestHistory(t *testing.T) {
	lines := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "l%d\r\n", i)
		}
		return b.String()
	}
	t.Run("scrolled lines", func(t *testing.T) {
		s := New(10, 3, 100)
		feedString(s, lines(5)+"x")
		if got, want := s.Capture(100), "l1\nl2\nl3\nl4\nl5\nx\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
		if got, want := s.Capture(2), "l2\nl3\nl4\nl5\nx\n"; got != want {
			t.Errorf("capture(2) = %q, want %q", got, want)
		}
	})
	t.Run("limit drops oldest", func(t *testing.T) {
		s := New(10, 2, 3)
		feedString(s, lines(10))
		if got, want := s.Capture(100), "l7\nl8\nl9\nl10\n\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("no history", func(t *testing.T) {
		s := New(10, 2, -5)
		feedString(s, lines(10)+"\x1b[2J")
		if got, want := s.Capture(100), "\n\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("ED 3 clears history", func(t *testing.T) {
		s := New(10, 2, 100)
		feedString(s, lines(5)+"\x1b[3J")
		if got, want := s.Capture(100), "l5\n\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("ED 2 scrolls used rows into history", func(t *testing.T) {
		s := New(10, 4, 100)
		feedString(s, "a\r\n   \r\nb\x1b[2J")
		if got, want := s.Capture(100), "a\n\nb\n\n\n\n\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("ED 2 skips unused rows below", func(t *testing.T) {
		s := New(10, 4, 100)
		feedString(s, "a\x1b[2;1H\x1b[2K\x1b[2J")
		if got, want := s.Capture(100), "a\n\n\n\n\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("ED 0 from home scrolls into history", func(t *testing.T) {
		s := New(10, 2, 100)
		feedString(s, "a\r\nb\x1b[H\x1b[J")
		if got, want := s.Capture(100), "a\nb\n\n\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("ED 0 elsewhere does not", func(t *testing.T) {
		s := New(10, 2, 100)
		feedString(s, "a\r\nb\x1b[1;2H\x1b[J")
		if got, want := s.Capture(100), "a\n\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("region at top feeds history", func(t *testing.T) {
		s := New(10, 3, 100)
		feedString(s, "\x1b[1;2r\x1b[2;1Ha\nb\nc\x1b[3;1Hz")
		if got, want := s.Capture(100), "\na\n b\n  c\nz\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("lower region does not", func(t *testing.T) {
		s := New(10, 3, 100)
		feedString(s, "top\x1b[2;3r\x1b[3;1Ha\r\nb\r\nc")
		if got, want := s.Capture(100), "top\nb\nc\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("DL never feeds history", func(t *testing.T) {
		s := New(10, 2, 100)
		feedString(s, "a\r\nb\x1b[H\x1b[M")
		if got, want := s.Capture(100), "b\n\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
	t.Run("history keeps wide and combining text", func(t *testing.T) {
		s := New(6, 1, 100)
		feedString(s, "中e\u0301x\r\nz")
		if got, want := s.Capture(1), "中e\u0301x\nz\n"; got != want {
			t.Errorf("capture = %q, want %q", got, want)
		}
	})
}

func TestResize(t *testing.T) {
	t.Run("clamps", func(t *testing.T) {
		s := New(0, -3, 0)
		if c, r := s.Size(); c != 1 || r != 1 {
			t.Errorf("size = %d,%d", c, r)
		}
		s.Resize(-1, 0)
		if c, r := s.Size(); c != 1 || r != 1 {
			t.Errorf("size = %d,%d", c, r)
		}
		s.Resize(1<<20, 1<<20)
		if c, r := s.Size(); c != maxDim || r != maxDim {
			t.Errorf("size = %d,%d", c, r)
		}
	})
	t.Run("shrink rows keeps cursor row", func(t *testing.T) {
		s := New(10, 5, 100)
		feedString(s, "a\r\nb\r\nc\r\nd\r\ne")
		s.Resize(10, 3)
		checkScreen(t, s, []string{"c", "d", "e"})
		if got := s.Capture(10); got != "a\nb\nc\nd\ne\n" {
			t.Errorf("history = %q", got)
		}
		if x, y := s.Cursor(); x != 1 || y != 2 {
			t.Errorf("cursor = %d,%d", x, y)
		}
	})
	t.Run("shrink rows drops below cursor first", func(t *testing.T) {
		s := New(10, 5, 100)
		feedString(s, "a\r\nb\r\nc\r\nd\r\ne\x1b[2;1H")
		s.Resize(10, 3)
		checkScreen(t, s, []string{"a", "b", "c"})
		if s.hist.len() != 0 {
			t.Errorf("history grew: %d", s.hist.len())
		}
		s.Resize(10, 2)
		checkScreen(t, s, []string{"a", "b"})
	})
	t.Run("grow rows pulls history back", func(t *testing.T) {
		s := New(10, 3, 100)
		feedString(s, "a\r\nb\r\nc\r\nd\r\ne")
		s.Resize(10, 6)
		checkScreen(t, s, []string{"a", "b", "c", "d", "e"})
		if x, y := s.Cursor(); x != 1 || y != 4 {
			t.Errorf("cursor = %d,%d", x, y)
		}
		if s.hist.len() != 0 {
			t.Errorf("history = %d", s.hist.len())
		}
	})
	t.Run("grow after clear does not pull back", func(t *testing.T) {
		s := New(10, 2, 100)
		feedString(s, "a\r\nb\x1b[2J\x1b[Hx")
		s.Resize(10, 4)
		checkScreen(t, s, []string{"x"})
	})
	t.Run("columns", func(t *testing.T) {
		s := New(6, 2, 100)
		feedString(s, "abc中\r\nxyz")
		s.Resize(4, 2)
		checkScreen(t, s, []string{"abc", "xyz"})
		s.Resize(8, 2)
		checkScreen(t, s, []string{"abc", "xyz"})
		feedString(s, "\x1b[1;1H12345678")
		checkScreen(t, s, []string{"12345678", "xyz"})
	})
	t.Run("cursor clamp and region reset", func(t *testing.T) {
		s := New(10, 5, 100)
		feedString(s, "\x1b[2;4r\x1b[5;10H")
		s.Resize(5, 5)
		if x, y := s.Cursor(); x != 4 || y != 4 {
			t.Errorf("cursor = %d,%d", x, y)
		}
		if s.top != 0 || s.bottom != 4 {
			t.Errorf("region = %d..%d", s.top, s.bottom)
		}
	})
	t.Run("tab stops", func(t *testing.T) {
		s := New(10, 1, 0)
		feedString(s, "\x1b[1;5H\x1bH")
		s.Resize(30, 1)
		feedString(s, "\r\tA\tB\tC\tD")
		checkScreen(t, s, []string{"    A   B       C       D"})
	})
	t.Run("on alternate screen", func(t *testing.T) {
		s := New(10, 4, 100)
		feedString(s, "a\r\nb\r\nc\r\nd\x1b[?1049h\x1b[4;1Halt")
		s.Resize(10, 2)
		checkScreen(t, s, []string{"", "alt"})
		feedString(s, "\x1b[?1049l")
		checkScreen(t, s, []string{"c", "d"})
		if x, y := s.Cursor(); x != 1 || y != 1 {
			t.Errorf("cursor = %d,%d", x, y)
		}
		if got := s.Capture(5); got != "a\nb\nc\nd\n" {
			t.Errorf("capture = %q", got)
		}
	})
	t.Run("pending wrap survives row change only", func(t *testing.T) {
		s := New(4, 3, 0)
		feedString(s, "abcd")
		s.Resize(4, 4)
		feedString(s, "e")
		checkScreen(t, s, []string{"abcd", "e"})
		s = New(4, 3, 0)
		feedString(s, "abcd")
		s.Resize(6, 3)
		feedString(s, "e")
		checkScreen(t, s, []string{"abce"})
	})
}

func TestWideInvariant(t *testing.T) {
	// Every wide lead must be followed by its pad and every pad preceded by a
	// lead, whatever happens to the row.
	s := New(7, 3, 10)
	for _, in := range []string{
		"中文測試中文", "\x1b[1;2H\x1b[2@", "\x1b[1;1H中\x1b[1;3H\x1b[P", "\x1b[3X",
		"a\ufe0f\x1b[1;7H\ufe0f", "\x1b[1;4H❤\ufe0f", "\x1b[2;6H中文", "\x1b[4h中文中\x1b[4l",
	} {
		feedString(s, in)
		s.Resize(5+len(in)%4, 3)
		checkWideInvariant(t, s)
	}
}

func checkWideInvariant(t *testing.T, s *Screen) {
	t.Helper()
	for _, b := range []*buffer{s.main, s.alt} {
		for y, l := range b.lines {
			if len(l.cells) != s.cols {
				t.Fatalf("row %d has %d cells, want %d", y, len(l.cells), s.cols)
			}
			for x, c := range l.cells {
				if c.flags&cellWide != 0 && (x+1 >= len(l.cells) || l.cells[x+1].flags&cellPad == 0) {
					t.Fatalf("row %d col %d: wide lead without pad", y, x)
				}
				if c.flags&cellPad != 0 && (x == 0 || l.cells[x-1].flags&cellWide == 0) {
					t.Fatalf("row %d col %d: pad without lead", y, x)
				}
			}
		}
	}
}
