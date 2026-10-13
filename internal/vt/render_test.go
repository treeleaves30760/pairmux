package vt

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// staleClient leaves a client terminal in a state Render must correct.
const staleClient = "\x1b[?6h\x1b[5;10r\x1b[4h\x1b(0\x1b)0\x0e\x1b[?7l\x1b[31;4;7mgarbage\x1b[?1h\x1b=" +
	"\x1b[?1003h\x1b[?1006h\x1b[?2004h\x1b[?1004h\x1b[?25l\x1b[5 q\x1b]2;stale\x07"

// checkRoundTrip renders a into a fresh screen of the same size (after
// putting it in the stale client state) and asserts that everything Render
// promises to restore matches.
func checkRoundTrip(t *testing.T, name string, a *Screen) {
	t.Helper()
	out := a.Render()
	for _, bad := range []string{"\x1b[?1049", "\x1b[?1047", "\x1b[?47"} {
		if bytes.Contains(out, []byte(bad)) {
			t.Fatalf("%s: Render contains a buffer switch %q", name, bad)
		}
	}
	b := New(a.cols, a.rows, 0)
	feedString(b, staleClient)
	b.Feed(out)

	if got, want := b.Capture(0), a.Capture(0); got != want {
		t.Fatalf("%s: capture after Render differs\n got %q\nwant %q", name, got, want)
	}
	for y := range a.buf.lines {
		ac, bc := a.buf.lines[y].cells, b.buf.lines[y].cells
		for x := range ac {
			if norm(ac[x]) != norm(bc[x]) {
				t.Fatalf("%s: cell %d,%d = %+v, want %+v", name, x, y, bc[x], ac[x])
			}
		}
	}
	ax, ay := a.Cursor()
	bx, by := b.Cursor()
	if ax != bx || ay != by || a.pendingWrap != b.pendingWrap {
		t.Fatalf("%s: cursor %d,%d pending=%v, want %d,%d pending=%v", name, bx, by, b.pendingWrap, ax, ay, a.pendingWrap)
	}
	am, bm := a.Modes(), b.Modes()
	am.AltScreen, bm.AltScreen = false, false
	if am != bm {
		t.Fatalf("%s: modes\n got %+v\nwant %+v", name, bm, am)
	}
	if a.pen != b.pen || a.top != b.top || a.bottom != b.bottom || a.cursorStyle != b.cursorStyle || a.gl != b.gl {
		t.Fatalf("%s: pen/region/cursor style/GL differ: %+v %d..%d %d %d vs %+v %d..%d %d %d", name,
			b.pen, b.top, b.bottom, b.cursorStyle, b.gl, a.pen, a.top, a.bottom, a.cursorStyle, a.gl)
	}
	for i := range a.g {
		if a.g[i] != b.g[i] {
			t.Fatalf("%s: G%d = %c, want %c", name, i, b.g[i], a.g[i])
		}
	}
	if a.title != "" && a.title != b.title {
		t.Fatalf("%s: title = %q, want %q", name, b.title, a.title)
	}
}

// norm makes a blank cell that Render had to draw (as a space, to carry its
// attributes) comparable with the never-written original.
func norm(c cell) cell {
	if c.r == 0 {
		c.r = ' '
	}
	return c
}

func TestRenderRoundTrip(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"text":             "hello\r\nworld",
		"colors":           "\x1b[1;31mred\x1b[0m \x1b[38;5;200;48;2;1;2;3mx\x1b[4:3;58:2::9:8:7mcurly\x1b[21;53;9mdbl\x1b[m",
		"colored blanks":   "\x1b[44m\x1b[2J\x1b[3;1H\x1b[41m\x1b[K\x1b[0m\x1b[H",
		"pending wrap":     "\x1b[3;1H\x1b[32m0123456789\x1b[0m",
		"pending wrap end": "\x1b[5;1H\x1b[7m0123456789",
		"bottom right":     strings.Repeat("abcdefghij", 5) + "\x1b[2;2H",
		"wide edge":        "\x1b[5;9H中\x1b[1;1H",
		"wide pending":     "\x1b[5;9H中",
		"combining":        "e\u0301 \x1b[4C\u0302 中\u0301 ❤\ufe0f \U0001f468\u200d\U0001f469 \U0001f1f9\U0001f1fc",
		"region":           "\x1b[2;4r\x1b[3;2Hx",
		"origin":           "\x1b[2;4r\x1b[?6h\x1b[2;3Hx",
		"origin pending":   "\x1b[2;4r\x1b[?6h\x1b[3;1H0123456789",
		"modes":            "\x1b[?1h\x1b=\x1b[?2004h\x1b[?1004h\x1b[?1002h\x1b[?1006h\x1b[4h\x1b[3 q\x1b[?25l",
		"nowrap":           "\x1b[?7l0123456789XYZ",
		"charsets":         "\x1b(0lqk\x1b)0\x0e",
		"charset G0 only":  "\x1b(0lqk",
		"G2 invoked":       "\x1b*0\x1bn",
		"title":            "\x1b]2;my title\x07x",
		"pen":              "x\x1b[1;3;38;2;9;9;9m",
		"alt":              "main\x1b[?1049h\x1b[3;3Halt\x1b[1m",
		"saved cursor":     "\x1b[2;2H\x1b7\x1b[4;4H",
	}
	for name, in := range cases {
		s := New(10, 5, 50)
		feedString(s, in)
		checkRoundTrip(t, name, s)
	}
}

func TestRenderRoundTripGenerated(t *testing.T) {
	for _, c := range diffCorpus() {
		s := New(c.cols, c.rows, 2000)
		s.Feed(c.data)
		checkRoundTrip(t, c.name, s)
	}
	for seed := 1000; seed < 1100; seed++ {
		rng := rand.New(rand.NewSource(int64(seed)))
		cols, rows := 12+rng.Intn(90), 4+rng.Intn(36) // randomStream needs some room
		data, _ := randomStream(seed, cols, rows, seed%2 == 0)
		s := New(cols, rows, 100)
		for _, ch := range randomChunks(data, rng, 50) {
			s.Feed(ch)
			if rng.Intn(20) == 0 {
				s.Resize(2+rng.Intn(100), 2+rng.Intn(40))
			}
		}
		checkRoundTrip(t, fmt.Sprintf("random-%d", seed), s)
	}
}

// TestRenderNoScroll checks that drawing the bottom-right cell does not
// scroll the client's screen.
func TestRenderNoScroll(t *testing.T) {
	a := New(4, 2, 0)
	feedString(a, "abcdefgh\x1b[H")
	b := New(4, 2, 0)
	b.Feed(a.Render())
	if got := b.Capture(0); got != "abcd\nefgh\n" {
		t.Errorf("capture = %q", got)
	}
}

func TestRenderSGRIsIncremental(t *testing.T) {
	s := New(20, 1, 0)
	feedString(s, "\x1b[1mab\x1b[31mcd\x1b[22mef")
	out := string(s.Render())
	// bold, then only the color is added, then a reset is needed to drop bold.
	if !strings.Contains(out, "\x1b[1mab\x1b[31mcd\x1b[0;31mef") {
		t.Errorf("unexpected SGR sequence in %q", out)
	}
}
