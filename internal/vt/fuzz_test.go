package vt

import (
	"hash/fnv"
	"math/rand"
	"strings"
	"testing"
)

// FuzzFeed checks that no input makes Feed, Resize, Capture or Render panic,
// that the screen's structural invariants hold throughout, and that Render
// reproduces the visible text. The input stream is fed in chunks, with
// resizes in between, chosen by a PRNG seeded from the input so that the
// stream itself stays intact for the fuzzer to mutate.
//
//	go test -run='^$' -fuzz=FuzzFeed -fuzztime=30s ./internal/vt
func FuzzFeed(f *testing.F) {
	for _, c := range diffCorpus()[:20] {
		if len(c.data) < 4096 {
			f.Add(c.data)
		}
	}
	for _, s := range []string{
		"", "\x1b", "\x1b[", "\x1b]", "\x1bP", "\xe4\xb8", "\x1b[?1049h\x1b[?1049l",
		"\x1b[6n\x1b]10;?\x07\x1bP$q\x1b\\\x1b[>q\x1b[?2004$p\x1b[18t", "中文\x1b[1;2H\x1b[@❤\ufe0f",
		"\x1b[2;4r\x1b[?6h\x1b[5L\x1b[3M\x1b[2S\x1b[3T", "\x1b#8\x1b[?7l\x1b[4h中文中文中文",
		"\U0001f468\u200d\U0001f469\U0001f1f9\U0001f1fc\U0001f1ef\x1b[3be\u0301\u0302",
		"\x1b(0lqk\x0e\x1b)0q\x0f\x1bc\x1b[!p\x1b[3J\x1b[2J", "\x1b]2;" + strings.Repeat("x", 300) + "\x1b\\",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		h := fnv.New64a()
		h.Write(data)
		rng := rand.New(rand.NewSource(int64(h.Sum64())))
		s := New(1+rng.Intn(30), 1+rng.Intn(12), rng.Intn(20))
		for _, chunk := range randomChunks(data, rng, 1+rng.Intn(64)) {
			s.Feed(chunk)
			switch rng.Intn(12) {
			case 0:
				s.Resize(1+rng.Intn(30), 1+rng.Intn(12))
			case 1:
				_ = s.Capture(rng.Intn(30))
			case 2:
				_ = s.Render()
			}
			checkInvariants(t, s)
		}
		checkRenderText(t, s)
	})
}

func checkInvariants(t *testing.T, s *Screen) {
	t.Helper()
	if s.cx < 0 || s.cx >= s.cols || s.cy < 0 || s.cy >= s.rows {
		t.Fatalf("cursor %d,%d outside %dx%d", s.cx, s.cy, s.cols, s.rows)
	}
	if s.pendingWrap && (s.cx != s.cols-1 || !s.modes.Autowrap) {
		t.Fatalf("pending wrap at column %d (autowrap %v)", s.cx, s.modes.Autowrap)
	}
	if s.top < 0 || s.bottom >= s.rows || s.top > s.bottom {
		t.Fatalf("scroll region %d..%d in %d rows", s.top, s.bottom, s.rows)
	}
	if len(s.tabs) != s.cols || len(s.main.lines) != s.rows || len(s.alt.lines) != s.rows {
		t.Fatalf("tabs %d, main %d, alt %d rows for %dx%d", len(s.tabs), len(s.main.lines), len(s.alt.lines), s.cols, s.rows)
	}
	if s.hist.len() > s.hist.limit {
		t.Fatalf("history %d over limit %d", s.hist.len(), s.hist.limit)
	}
	checkWideInvariant(t, s)
}

// checkRenderText renders into a fresh screen and compares the visible text.
// Screens holding hand-made adjacencies that Render cannot replay in order
// (a glyph placed right after a ZWJ or next to a lone regional indicator, or
// a narrow glyph carrying a VS16 because it once sat in the last column) are
// skipped: drawing them left to right would join or widen them.
func checkRenderText(t *testing.T, s *Screen) {
	t.Helper()
	for _, l := range s.buf.lines {
		for _, c := range l.cells {
			if strings.ContainsAny(c.comb, "\u200d\ufe0f") || isRegionalIndicator(c.r) {
				return
			}
		}
	}
	b := New(s.cols, s.rows, 0)
	b.Feed(s.Render())
	if got, want := b.Capture(0), s.Capture(0); got != want {
		t.Fatalf("Render does not reproduce the screen\n got %q\nwant %q", got, want)
	}
	if b.cx != s.cx || b.cy != s.cy || b.pendingWrap != s.pendingWrap {
		t.Fatalf("Render cursor %d,%d/%v, want %d,%d/%v", b.cx, b.cy, b.pendingWrap, s.cx, s.cy, s.pendingWrap)
	}
}
