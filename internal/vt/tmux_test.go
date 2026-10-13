package vt

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The differential test runs each corpus stream through a real tmux (in an
// isolated server on its own socket) and through Screen, then compares
// `tmux capture-pane -p` with Capture, including history via -S, plus the
// cursor position and a few modes.
//
// Known, deliberate differences are kept out of the corpus (or normalized)
// and documented here, because in each case the emulator follows xterm or the
// package specification rather than a tmux-specific behavior:
//
//   - tmux 3.5+ stores a tab written over empty cells as a single tab cell
//     and capture-pane prints it as '\t'; Capture prints the spaces it covers.
//     tmux output is normalized by expanding tabs to the default 8-column
//     stops, so tab tests over empty cells only use default stops, and the
//     generated streams use no tabs (a tab cell that is later partly erased
//     or overwritten cannot be expanded back).
//   - tmux stores DEC Special Graphics as ASCII with a charset attribute and
//     capture-pane prints "lqqk"; Capture prints box drawing. Cases marked dec
//     map our box drawing back to ASCII before comparing.
//   - tmux keeps the main screen's history visible to capture-pane -S while
//     the alternate screen is active; Capture shows no history there.
//   - tmux pushes rows scrolled out of a scroll region into the history even
//     when the region does not start at the top row; Capture (like xterm) only
//     does so when the region starts at row 0. History is only compared for
//     cases that never scroll a region with top > 0.
//   - Pending wrap (a glyph just written in the last column): tmux keeps the
//     wrap pending across LF, BS, CUB/CUU/CUD, EL, ECH, ICH/DCH, IL/DL and DECSC
//     does not save it; xterm (and Screen) cancel it on cursor movement and
//     erasure, EL 0 erases the last glyph, and DECSC/DECRC preserve it. The
//     corpus only follows a last-column glyph with CR, CUP, printable text, or
//     sequences that behave identically.
//   - Backspace in column 0 when the previous row wrapped: tmux moves to the
//     end of that row (reverse wraparound); xterm, by default, and Screen stay
//     put. The corpus never backspaces from column 0 after a wrapped row.
//   - tmux does not implement CHT (CSI I), HPR (CSI a), VPR (CSI e), DECST8C
//     (CSI ? 5 W) or ?1048, clamps REP to the end of the line, and reads a
//     DECSTBM bottom margin of 0 as row 1 instead of the last row. These are
//     covered by unit tests only.
//   - tmux does not clear the vacated cells or lines when an ICH (near the
//     right margin) or an IL/DL (outside the scroll region) count exceeds the
//     cells or lines left to shift. The corpus keeps such counts small.
//   - Erasing, inserting or deleting at half of a wide glyph: tmux leaves the
//     other half behind; Screen clears it. Generated streams with wide text
//     therefore avoid partial erases, ICH, DCH and ECH.
//   - ?47h re-entering the alternate screen: tmux always clears it, xterm
//     keeps its previous contents. Not exercised.
//   - VS16 after a glyph in the last column and Hangul jamo that do not follow
//     a leading consonant: tmux-specific edge cases, not exercised.
//   - DECCOLM (?3h/l) clears the screen in tmux; Screen ignores it (it is
//     filtered from the human's terminal too). Not exercised.
//   - tmux's history limit trims 10% at a time, so history comparisons ask
//     for at most 90% of the limit.
//   - tmux built with sixel support moves the cursor below a sixel image;
//     the corpus uses a non-sixel DCS.
func TestDifferentialTmux(t *testing.T) {
	if testing.Short() {
		t.Skip("differential tmux test skipped in -short mode")
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not on PATH")
	}
	sock := fmt.Sprintf("pairmux-vt-%d-%d", os.Getpid(), time.Now().UnixNano()%100000)
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
			env = append(env, kv)
		}
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(tmuxPath, append([]string{"-L", sock, "-f", "/dev/null"}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out), nil
	}
	t.Cleanup(func() { _, _ = run("kill-server") })

	dir := t.TempDir()
	cases := diffCorpus()
	const marker = "pairmux-vt-done"
	// Sessions are started in batches (each holds a PTY, and macOS has only
	// a few hundred) and killed once compared.
	const batch = 64
	for lo := 0; lo < len(cases); lo += batch {
		hi := min(lo+batch, len(cases))
		for i := lo; i < hi; i++ {
			c := cases[i]
			f := filepath.Join(dir, fmt.Sprintf("case%04d.bin", i))
			if err := os.WriteFile(f, c.data, 0o600); err != nil {
				t.Fatal(err)
			}
			// -opost stops the tty from turning LF into CRLF; the OSC 2
			// marker arrives after the stream in the same byte order, so
			// seeing it as the pane title proves tmux has processed
			// everything before it.
			sh := fmt.Sprintf("stty -opost; cat '%s'; printf '\\033]2;%s\\007'; exec sleep 600", f, marker)
			if _, err := run("new-session", "-d", "-s", fmt.Sprintf("c%04d", i),
				"-x", strconv.Itoa(c.cols), "-y", strconv.Itoa(c.rows), sh); err != nil {
				t.Fatal(err)
			}
		}
		for i := lo; i < hi; i++ {
			c := cases[i]
			target := fmt.Sprintf("c%04d", i)
			waitForMarker(t, run, target, marker, c.name)
			t.Run(c.name, func(t *testing.T) { compareWithTmux(t, run, target, c, i) })
			_, _ = run("kill-session", "-t", target)
		}
	}
}

func waitForMarker(t *testing.T, run func(...string) (string, error), target, marker, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		title, err := run("display-message", "-p", "-t", target, "#{pane_title}")
		if err == nil && strings.TrimSpace(title) == marker {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: tmux never finished the stream (title %q, err %v)", name, title, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func compareWithTmux(t *testing.T, run func(...string) (string, error), target string, c diffCase, i int) {
	s := New(c.cols, c.rows, 2000)
	feedChunked(s, c.data, rand.New(rand.NewSource(int64(i))))

	want, err := run("capture-pane", "-p", "-t", target)
	if err != nil {
		t.Fatal(err)
	}
	compareCapture(t, "visible", normalizeTmux(want, c.cols), normalizeOurs(s.Capture(0), c.dec))
	if c.hist > 0 && !s.Modes().AltScreen {
		want, err := run("capture-pane", "-p", "-t", target, "-S", strconv.Itoa(-c.hist))
		if err != nil {
			t.Fatal(err)
		}
		compareCapture(t, "history", normalizeTmux(want, c.cols), normalizeOurs(s.Capture(c.hist), c.dec))
	}
	st, err := run("display-message", "-p", "-t", target,
		"#{cursor_x} #{cursor_y} #{alternate_on} #{cursor_flag} #{keypad_cursor_flag} #{keypad_flag} #{insert_flag} #{wrap_flag} #{origin_flag}")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(st)
	if len(f) != 9 {
		t.Fatalf("unexpected display-message output %q", st)
	}
	x, _ := strconv.Atoi(f[0])
	y, _ := strconv.Atoi(f[1])
	x = min(x, c.cols-1) // tmux shows a pending wrap as column == width
	m := s.Modes()
	gx, gy := s.Cursor()
	const format = "cursor=%d,%d alt=%v vis=%v ckm=%v kp=%v irm=%v wrap=%v origin=%v"
	got := fmt.Sprintf(format, gx, gy, m.AltScreen, m.CursorVisible, m.AppCursor, m.AppKeypad, m.Insert, m.Autowrap, m.OriginMode)
	wantSt := fmt.Sprintf(format, x, y, f[2] == "1", f[3] == "1", f[4] == "1", f[5] == "1", f[6] == "1", f[7] == "1", f[8] == "1")
	if got != wantSt {
		t.Errorf("state mismatch\n tmux: %s\n   vt: %s", wantSt, got)
	}
}

type diffCase struct {
	name       string
	data       []byte
	cols, rows int
	hist       int  // history lines to compare; 0 skips the history comparison
	dec        bool // the stream draws with DEC Special Graphics
}

// feedChunked feeds data in random-sized chunks so sequences and UTF-8
// characters straddle Feed calls.
func feedChunked(s *Screen, data []byte, rng *rand.Rand) {
	for len(data) > 0 {
		n := 1 + rng.Intn(64)
		if n > len(data) {
			n = len(data)
		}
		s.Feed(data[:n])
		data = data[n:]
	}
}

func compareCapture(t *testing.T, what, want, got string) {
	t.Helper()
	if want == got {
		return
	}
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	var b strings.Builder
	for i := 0; i < max(len(wl), len(gl)); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		mark := " "
		if w != g {
			mark = "!"
		}
		fmt.Fprintf(&b, "%s %3d tmux %q\n%s     vt   %q\n", mark, i, w, mark, g)
	}
	t.Errorf("%s capture mismatch (%d vs %d lines):\n%s", what, len(wl), len(gl), b.String())
}

// normalizeTmux expands tmux's tab cells (see the test comment) to spaces at
// the default 8-column tab stops (a tab never moves past the last column)
// and re-trims trailing spaces.
func normalizeTmux(s string, cols int) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if !strings.Contains(l, "\t") {
			continue
		}
		var b strings.Builder
		col := 0
		for _, r := range l {
			if r == '\t' {
				n := min(8-col%8, cols-1-col)
				b.WriteString(strings.Repeat(" ", n))
				col += n
				continue
			}
			b.WriteRune(r)
			col += RuneWidth(r)
		}
		lines[i] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(lines, "\n")
}

// normalizeOurs maps box drawing back to the DEC Special Graphics ASCII
// letters tmux prints, for cases drawn with that character set.
func normalizeOurs(s string, dec bool) string {
	if !dec {
		return s
	}
	rev := map[rune]rune{}
	for i, r := range decGraphics {
		rev[r] = rune(0x5f + i)
	}
	var b strings.Builder
	for _, r := range s {
		if a, ok := rev[r]; ok {
			r = a
		}
		b.WriteRune(r)
	}
	return b.String()
}

func cat(parts ...string) []byte { return []byte(strings.Join(parts, "")) }

func numberedLines(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d: the quick brown fox jumps over the lazy dog\r\n", prefix, i)
	}
	return b.String()
}

func diffCorpus() []diffCase {
	rep := strings.Repeat
	cs := []diffCase{
		{name: "shell", hist: 200, data: cat(
			"$ ls -la\r\n", "total 48\r\n",
			"drwxr-xr-x  5 user  staff   160 Oct 14 10:00 .\r\n",
			"drwxr-xr-x 12 user  staff   384 Oct 14 09:00 ..\r\n",
			"-rw-r--r--  1 user  staff  1024 Oct 14 10:00 README.md\r\n",
			"$ seq 40\r\n", seqLines(40), "$ echo done\r\ndone\r\n$ ")},
		{name: "wrap-long", hist: 100, data: cat(
			rep("abcdefghij", 25), "\r\n", rep("x", 80), "\r\n", rep("y", 81), "\r\nend\r\n",
			rep("0123456789", 30), "\r\n")},
		{name: "exact-width-then-text", data: cat(rep("z", 80), "next", "\r\n", rep("w", 160), "\r\nafter")},
		{name: "exact-width-crlf", data: cat(rep("z", 80), "\r\n", "next\r\n", rep("q", 80), "\r", "over")},
		{name: "cjk", hist: 50, data: cat(
			"中文測試 mixed 中文\r\n", rep("a", 79), "中b\r\n", rep("中", 41), "\r\n",
			"ab中文cd\r\n", "한국어 텍스트\r\n", "日本語のテキスト、カタカナ\r\n", "ＦＵＬＬ　ｗｉｄｔｈ\r\n")},
		{name: "cjk-overwrite", data: cat(
			"中文測試\x1b[1;2Hx\x1b[1;5Hy\r\n", "中文測試\x1b[2;3Hz\r\n",
			"中文測試\x1b[3;1H\x1b[4C中\r\n", rep("中", 40), "\x1b[4;80Hk")},
		{name: "emoji", data: cat(
			"😀 smile ⌚ watch 👍\U0001f3fd skin\r\n", "❤\ufe0f heart ❤ plain ☀\ufe0f sun\r\n",
			"👨\u200d👩\u200d👧 family 🇹🇼 flag 🏳\ufe0f\u200d🌈 rainbow\r\n", "e\u0301 cafe\u0301 n\u0303\r\n",
			"#\ufe0f\u20e3 keycap ✔\ufe0f check\r\n", rep("😀", 45), "\r\n")},
		{name: "tabs", data: cat(
			"a\tb\tc\r\n", "\tindented\r\n", "12345678901\tX\r\n", rep("x", 75), "\tY\r\n",
			"\t\t\t\t\t\t\t\t\t\t\tZ\r\n", "ab\tcd\x1b[3D\tef\r\n")},
		{name: "tabs-custom", data: cat(
			"\x1b[3g\x1b[1;5H\x1bH\x1b[1;20H\x1bH\x1b[1;33H\x1bH",
			"\x1b[2;1H", rep(".", 40), "\r\tA\tB\tC\tD\r\n",
			rep(".", 79), "\r\t\tX\x1b[ZY\x1b[2ZW\r\n",
			rep(",", 60), "\r\tP\tQ\tR\r\n",
			rep(".", 30), "\r\x1b[1;20H\x1b[0g\x1b[4;1H\tS\tT\r\n")},
		{name: "cursor-moves", data: cat(
			"\x1b[5;10Hhere\x1b[2Aup\x1b[3Bdown\x1b[5Cfwd\x1b[20Dback\x1b[2Enext\x1b[1Fprev",
			"\x1b[30Gcol30\x1b[10dvpa\x1b[999;999H\x1b[2DXY\r",
			"\x1b[0;0Horigin\x1b[H\x1b[999Aup\x1b[999Cright\r\x1b[999Bbottom\x1b[12;40f HVP",
			"\x1b[15`backtick\x1b[;5Hsemi")},
		{name: "erase", data: cat(
			fullScreen('#', 80, 24),
			"\x1b[3;5H\x1b[0K\x1b[4;5H\x1b[1K\x1b[5;5H\x1b[2K\x1b[6;2H\x1b[3X\x1b[7;70H\x1b[50X",
			"\x1b[10;10H\x1b[1J\x1b[20;20H\x1b[J\x1b[8;8HX")},
		{name: "erase-bg", data: cat("\x1b[41mred\x1b[2J\x1b[0mX\x1b[44m\x1b[3;1H\x1b[K\x1b[0m\r\nline\r\n")},
		{name: "ich-dch", data: cat(
			"abcdefghij\r\x1b[3@XY\r\n", "abcdefghij\r\x1b[3P\r\n", "中文測試\x1b[3;3H\x1b[1@\r\n",
			"中文\r\x1b[2P\r\n", "中文測試\x1b[5;3H\x1b[2X\r\n", "abcdef\x1b[6;3H\x1b[100P\r\n",
			"abcdef\x1b[7;3H\x1b[70@\r\n", "中文測試\x1b[8;3H\x1b[2P\r\n", rep("a", 76), "中\r\x1b[1@\r\n")},
		{name: "scroll-region-mid", data: cat(
			fullScreen('o', 80, 24), "\x1b[5;10r\x1b[10;1H", numberedLines("r", 8),
			"\x1b[5;1H\x1bM\x1bMtop\x1b[2S\x1b[1T\x1b[7;1H\x1b[2L\x1b[1M\x1b[r\x1b[24;1Hend")},
		{name: "scroll-region-top0", hist: 100, data: cat(
			"header\r\n\x1b[1;10r\x1b[10;1H", numberedLines("t", 20), "\x1b[r\x1b[20;1Hbelow")},
		{name: "scroll-region-bottom", hist: 100, data: cat(
			numberedLines("pre", 5), "\x1b[24;1Hstatus line\x1b[1;23r\x1b[23;1H", numberedLines("log", 30), "\x1b[r")},
		{name: "il-dl", data: cat(
			numberedLines("l", 10), "\x1b[3;5H\x1b[2L\x1b[6;1H\x1b[3M\x1b[1;1H\x1b[30L\x1b[24;1H\x1b[1L\x1b[1;1H\x1b[M")},
		{name: "il-dl-outside-region", data: cat(
			numberedLines("o", 12), "\x1b[2;6r\x1b[9;3H\x1b[2L\x1b[11;1H\x1b[1M\x1b[1;1H\x1b[L\x1b[r")},
		{name: "su-sd", hist: 100, data: cat(numberedLines("s", 30), "\x1b[3S\x1b[1T\x1b[50S\x1b[3;1Hafter")},
		{name: "alt-1049", hist: 100, data: cat(
			numberedLines("m", 30), "\x1b[?1049h\x1b[Halt screen\r\nmore alt\r\n", numberedLines("a", 30),
			"\x1b[?1049lback\r\n")},
		{name: "alt-1049-active", data: cat(
			"main text\r\n\x1b[?1049h\x1b[2J\x1b[H", numberedLines("alt", 30), "\x1b[5;5Hcursor")},
		{name: "alt-1047", hist: 50, data: cat("m1\r\nm2\x1b[?1047hALT TEXT\x1b[?1047lmain-again\r\n")},
		{name: "alt-47", data: cat("m1\r\nm2\x1b[?47hALT\x1b[?47lback")},
		{name: "alt-combined", hist: 50, data: cat("m\r\n\x1b[?1049;1hX\x1b[?1;1049lY")},
		{name: "linedraw", dec: true, data: cat(
			"\x1b(0lqqqk\r\nx   x\r\nmqqqj\x1b(B normal\r\n", "\x1b)0\x0elqwqk\x0f text\r\n",
			"\x1b(0`abcdefghijklmnopqrstuvwxyz{|}~\x1b(B\r\n")},
		{name: "sgr", data: cat(
			"\x1b[1;31mred bold\x1b[0m \x1b[38;5;200mpink\x1b[48;2;10;20;30m bg\x1b[38:2::1:2:3m colon",
			"\x1b[4:3mcurly\x1b[m done\r\n", "\x1b[3;4;5;7;8;9;53mattrs\x1b[22;23;24;25;27;28;29;55m off\x1b[0m\r\n",
			"\x1b[90;100mbright\x1b[39;49m\x1b[58;5;3m\x1b[58:2::1:2:3m\x1b[59m\x1b[21mdbl\x1b[m\r\n",
			"\x1b[38;5mtrunc\x1b[38;2;1;2mtrunc2\x1b[mend")},
		{name: "decsc", data: cat(
			"\x1b[3;3H\x1b[1msaved\x1b7\x1b[10;10H\x1b[0mmoved\x1b8restored\x1b[s\x1b[5;5Hx\x1b[uY",
			"\x1b[12;12Hq\x1b8Z")},
		{name: "origin", data: cat(
			"\x1b[5;15r\x1b[?6h\x1b[1;1Hfirst-in-region\x1b[20;1Hclamped\x1b[3dvpa",
			"\x1b[?6l\x1b[1;1Habs\x1b[r\x1b[22;1Hend")},
		{name: "history-clear", hist: 200, data: cat(numberedLines("h", 30), "\x1b[H\x1b[2Jafter clear\r\n")},
		{name: "history-clear-partial", hist: 200, data: cat("a\r\nb\r\n   \r\n\r\nc\x1b[2;1H\x1b[2K\x1b[2Jx")},
		{name: "history-ed0-home", hist: 200, data: cat(numberedLines("e", 5), "\x1b[H\x1b[Jtop\r\n")},
		{name: "history-ed3", hist: 200, data: cat(numberedLines("h", 30), "\x1b[3Jx")},
		{name: "history-overflow", hist: 1700, data: cat(numberedLines("big", 2100))},
		{name: "rep", data: cat("ab\x1b[5b\r\n中\x1b[3b\r\n-\x1b[79b\r\nx\r\x1b[2b")},
		{name: "decaln", data: cat("\x1b[5;10r\x1b#8\x1b[5;5Hhello\x1b[30;1Hbottom")},
		{name: "nowrap", data: cat("\x1b[?7l", rep("w", 100), "\r\n", rep("v", 79), "中\r\n\x1b[?7hok")},
		{name: "insert", data: cat("hello world\r\x1b[4hINS \x1b[4l\r\n", "abc\r\x1b[4h中\x1b[4l\r\n")},
		{name: "progress", data: cat("50%\r100%\r\n", "downloading... [####      ]\r[##########]\r\n")},
		{name: "backspace", data: cat("abc\b\bX\r\n\b\bstart\r\nab\x08\x08\x08\x08c")},
		{name: "lf-only", data: cat("a\nb\nc\x0bd\x0ce")},
		{name: "nel-ind-ri", data: cat("one\x1bEnel\x1bDind\x1bMri\x1b[H\x1bM\x1bMtop")},
		{name: "osc-noise", data: cat(
			"\x1b]0;my title\x07text\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\ \x1b]133;A\x07$ \r\n",
			"\x1b]7779;p;0\x07\x1b]52;c;aGVsbG8=\x07after\r\n")},
		{name: "string-noise", data: cat(
			"\x1b_Gi=1;AAAA\x1b\\x\x1b^pm\x1b\\y\x1bXsos\x1b\\z\x1bP1zdata\x07more\x1b\\done")},
		{name: "csi-private-noise", data: cat(
			"\x1b[>4;1m\x1b[>1u\x1b[<u\x1b[?2026h\x1b[?2026lvisible\x1b[>4n\x1b[?1004h\x1b[?1004l")},
		{name: "combining", data: cat(
			"e\u0301\u0302x\r\n\u0301start\r\nab\x1b[D\u0301\r\n", "ab\x1b[5C\u0301x\r\n",
			"中\x1b[D\u0301\r\n", "中\u0301\r\n", "क\u094dष hindi\r\n")},
		{name: "zero-width", data: cat("a\u200bb\u2060c\ufeffd\r\n\u200bstart\r\n")},
		{name: "wide-last-col", data: cat(rep("a", 79), "中", "\r\n", rep("b", 78), "中", "x")},
		{name: "ris", hist: 100, data: cat(numberedLines("pre", 3), "\x1b[?1h\x1b=\x1b[4h\x1b[5;10rgarbage\x1bcafter reset")},
		{name: "modes", data: cat("\x1b[?1h\x1b=\x1b[4h\x1b[?25l\x1b[?7lmodes")},
		{name: "decstbm-invalid", data: cat("\x1b[10;5r\x1b[5;5r\x1b[3;3Hx\x1b[;rY\x1b[4;30r\x1b[2;2HZ")},
		{name: "small-screen", cols: 10, rows: 3, hist: 50, data: cat(
			"中文測試中文測試中文\r\n", "0123456789abcdefghij\r\n", "\x1b[1;2r\x1b[2;1H1\n2\n3\n4", "\x1b[r\x1b[1;1H\x1b[2J")},
		{name: "small-screen-region", cols: 10, rows: 3, data: cat(
			"中文測試中文測試中文\r\n", "0123456789abcdefghij\r\n", "\x1b[2;3r\x1b[3;1H1\n2\n3\n4")},
	}
	// VT_DIFF_SEEDS raises the number of generated streams for a longer
	// exploratory run (e.g. VT_DIFF_SEEDS=1000).
	seeds := 40
	if v, err := strconv.Atoi(os.Getenv("VT_DIFF_SEEDS")); err == nil && v > 0 {
		seeds = v
	}
	for seed := 1; seed <= seeds; seed++ {
		data, hist := randomStream(seed, 80, 24, seed%2 == 0)
		c := diffCase{name: fmt.Sprintf("random-%02d", seed), data: data, dec: true}
		if hist {
			c.hist = 500
		}
		cs = append(cs, c)
	}
	for i := range cs {
		if cs[i].cols == 0 {
			cs[i].cols, cs[i].rows = 80, 24
		}
	}
	return cs
}

func seqLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d\r\n", i)
	}
	return b.String()
}

func fullScreen(ch byte, cols, rows int) string {
	var b strings.Builder
	b.WriteString("\x1b[H")
	for y := 0; y < rows; y++ {
		fmt.Fprintf(&b, "\x1b[%d;1H%s", y+1, strings.Repeat(string(ch), cols))
	}
	b.WriteString("\x1b[H")
	return b.String()
}

// randomStream generates a deterministic stream from a vocabulary of
// operations that tmux and xterm agree on (see the test comment). Streams
// with wide characters leave out partial erases, ICH, DCH and ECH: when those
// land on half of a wide glyph tmux leaves the other half behind (an orphaned
// right half prints nothing, an orphaned left half still prints its glyph),
// whereas Screen clears both halves as the specification requires; curated
// cases cover those operations on narrow text. It reports whether history
// may be compared: false once a scroll region with a top margin below row 0
// was set.
func randomStream(seed, cols, rows int, wide bool) ([]byte, bool) {
	data, histOK, _ := randomStreamOps(seed, cols, rows, wide)
	return data, histOK
}

// randomStreamOps is randomStream that also returns the offset at which each
// operation ends, for bisecting a mismatch to the operation that caused it.
func randomStreamOps(seed, cols, rows int, wide bool) ([]byte, bool, []int) {
	var ends []int
	rng := rand.New(rand.NewSource(int64(seed)))
	var b strings.Builder
	histOK := true
	inAlt := false
	regTop, regBot := 0, rows-1 // the scroll region the stream has set
	words := []string{"hello", "world", "naïve", "e\u0301", "über", "-", "|", "Ω", "x", "pairmux", "a\u0308"}
	if wide {
		words = append(words, "中文", "測試", "😀", "한글", "　", "⌚")
	}
	text := func() {
		n := 1 + rng.Intn(12)
		for i := 0; i < n; i++ {
			b.WriteString(words[rng.Intn(len(words))])
			if rng.Intn(3) == 0 {
				b.WriteByte(' ')
			}
		}
		// Resynchronize: a pending wrap behaves differently in tmux.
		switch rng.Intn(3) {
		case 0:
			b.WriteString("\r\n")
		case 1:
			b.WriteString("\r")
		default:
			fmt.Fprintf(&b, "\x1b[%d;%dH", 1+rng.Intn(rows), 1+rng.Intn(cols))
		}
	}
	partial := func(n int) int {
		if wide {
			return 2
		}
		return rng.Intn(n)
	}
	for op := 0; op < 300; op++ {
		ends = append(ends, b.Len())
		switch rng.Intn(24) {
		case 0, 1, 2, 3, 4, 5:
			text()
		case 6:
			fmt.Fprintf(&b, "\x1b[%d;%dH", 1+rng.Intn(rows+2), 1+rng.Intn(cols+2))
		case 7:
			fmt.Fprintf(&b, "\x1b[%d%c", rng.Intn(10), "ABCDEFGd"[rng.Intn(8)])
		case 8:
			fmt.Fprintf(&b, "\x1b[%dK", partial(3))
		case 9:
			if rng.Intn(4) == 0 {
				fmt.Fprintf(&b, "\x1b[%dJ", partial(3))
			}
		case 10:
			// tmux mishandles IL/DL outside the scroll region and ICH near
			// the right margin when the count exceeds the lines or cells left
			// to move (the vacated area is not cleared), so place the cursor
			// where tmux and xterm agree.
			ops := "@PXLM"
			if wide {
				ops = "LM"
			}
			op := ops[rng.Intn(len(ops))]
			switch op {
			case 'L', 'M':
				fmt.Fprintf(&b, "\x1b[%d;%dH", regTop+1+rng.Intn(regBot-regTop+1), 1+rng.Intn(cols))
			case '@':
				fmt.Fprintf(&b, "\x1b[%d;%dH", 1+rng.Intn(rows), 1+rng.Intn(cols-10))
			}
			fmt.Fprintf(&b, "\x1b[%d%c", rng.Intn(5), op)
		case 11:
			fmt.Fprintf(&b, "\x1b[%d;%d;%dm", rng.Intn(10), 30+rng.Intn(8), 40+rng.Intn(8))
		case 12:
			top := 1 + rng.Intn(rows/2)
			bot := top + 1 + rng.Intn(rows-top)
			if rng.Intn(2) == 0 {
				top = 1
			}
			if top > 1 {
				histOK = false
			}
			if bot > rows {
				bot = rows
			}
			if top < bot {
				regTop, regBot = top-1, bot-1
			}
			fmt.Fprintf(&b, "\x1b[%d;%dr", top, bot)
		case 13:
			b.WriteString("\x1b[r")
			regTop, regBot = 0, rows-1
		case 14:
			b.WriteString("\r\n")
		case 15:
			b.WriteString("\x1bM")
		case 16:
			// No HT: tmux records a tab over empty cells as one tab cell,
			// and a later partial overwrite or erase of it leaves capture
			// output that no tab expansion can line up again.
			fmt.Fprintf(&b, "\x1b[%dGab\r", 1+rng.Intn(cols-2))
		case 17:
			fmt.Fprintf(&b, "\x1b[%d%c", rng.Intn(4), "ST"[rng.Intn(2)])
		case 18:
			b.WriteString("\x1b7")
			fmt.Fprintf(&b, "\x1b[%d;%dH", 1+rng.Intn(rows), 1+rng.Intn(cols))
			b.WriteString("saved\r\x1b8")
		case 19:
			b.WriteString(numberedLines("n", 1+rng.Intn(30)))
		case 20:
			b.WriteString("\x1b(0lqqk\x1b(B\r")
		case 21:
			if !wide {
				fmt.Fprintf(&b, "\x1b[%dX", rng.Intn(20))
			}
		case 22:
			// Never at column 0: tmux wraps a backspace there back onto the
			// previous row when that row wrapped (xterm's reverse-wraparound,
			// which xterm leaves off by default).
			fmt.Fprintf(&b, "\x1b[%d;%dH\b", 1+rng.Intn(rows), 2+rng.Intn(cols-1))
		case 23:
			// Only leave the alternate screen when on it: tmux and xterm
			// disagree about which saved cursor a stray ?1049l restores.
			switch {
			case inAlt:
				b.WriteString("\x1b[?1049l")
				inAlt = false
			case rng.Intn(4) == 0:
				b.WriteString("\x1b[?1049h")
				inAlt = true
			}
		}
	}
	data := []byte(b.String())
	if !utf8.Valid(data) {
		panic("random stream must be valid UTF-8")
	}
	return data, histOK, append(ends[1:], len(data))
}
